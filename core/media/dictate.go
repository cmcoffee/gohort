// Dictation: the web chat's 🎤 (DictationScript). The page records a clip with
// the browser's own recorder and posts it here; the deployment's transcription
// turns it into text, which the page puts in the input box to be read over and
// sent. The same Whisper that hears a voice memo hears this, so a recording
// never leaves for a browser vendor's speech service.

package media

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/cmcoffee/snugforge/nfo"
)

// DictatePath is where the chat page posts a recording to have it written out.
const DictatePath = "/api/dictate"

// dictateMaxBytes bounds one recording: the page stops itself at five
// minutes, which in any browser's format is a few megabytes.
const dictateMaxBytes = 25 << 20

// ServeDictation answers a POST of a recording (the body is the audio, its
// Content-Type the browser's format) with {"text": ...}. user is the
// signed-in user, empty when nobody is.
func ServeDictation(w http.ResponseWriter, r *http.Request, user string) {
	if user == "" {
		http.Error(w, "sign in first", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !GetTranscribeConfig().Enabled {
		http.Error(w, "transcription is not set up: an admin turns it on under Capabilities, Audio Transcription", http.StatusServiceUnavailable)
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, dictateMaxBytes))
	if err != nil {
		http.Error(w, "the recording is too large: dictation takes up to five minutes", http.StatusRequestEntityTooLarge)
		return
	}
	if len(audio) == 0 {
		http.Error(w, "the recording is empty", http.StatusBadRequest)
		return
	}
	// Browsers record webm/opus or mp4/aac, which a stock whisper.cpp does
	// not decode; 16kHz mono WAV is what it always takes. The raw clip goes
	// when ffmpeg is missing, named for what the browser said it is.
	send, name := audio, dictationFileName(r.Header.Get("Content-Type"))
	if wav, terr := TranscodeAudioToWAV(audio); terr == nil && len(wav) > 0 {
		send, name = wav, "dictation.wav"
	}
	note := dictationMeterNote(r.Header.Get("X-Dictation-Meter"))
	// A clip with no word in it is answered as heard empty, without the
	// transcriber, which writes words it made up for one ("Thank you."): one
	// too short to hold a word, or, where its samples can be read, one with
	// no voice in it. The page's meter cannot always tell; a web view's can
	// read nothing but zeros.
	levels := ""
	if clip, ok := readWAV(send); ok {
		secs := clip.seconds()
		voiced, peak, heard := clip.voice()
		why := ""
		switch {
		case secs < dictateMinSecs:
			why = "too short to hold a word"
		case heard && voiced < dictateMinVoicedMs:
			why = "no voice in it"
		}
		if heard {
			levels = fmt.Sprintf("%.1fs, %dms voiced, peak %.0f dBFS", secs, voiced, dbfs(peak))
		} else {
			levels = fmt.Sprintf("%.1fs", secs)
		}
		if why != "" {
			nfo.Debug("[dictate] %s: %s, %s, not sent; meter %s", user, levels, why, note)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(map[string]string{"text": ""})
			return
		}
	}
	prompt := dictationPrompt(dictationPageOf(r, user))
	ctx := WithTranscribePrompt(r.Context(), prompt)
	text, err := Transcribe(ctx, send, name)
	if err != nil {
		if r.Context().Err() == nil {
			nfo.Log("[dictate] transcription for %s failed: %v", user, err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	nfo.Debug("[dictate] %s: %d bytes, %s, meter %s, prompt %q, %d chars back", user, len(audio), levels, note, prompt, len(strings.TrimSpace(text)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"text": strings.TrimSpace(text)})
}

// DictationPage is the page a recording was made on: who is signed in, its
// path, and the fields the chat's next send would carry (an agent picker's
// choice, say). The page sends the path and fields; the request is the
// signed-in request itself, for a resolver that checks what the user may
// reach.
type DictationPage struct {
	User    string
	Path    string
	Fields  map[string]string
	Request *http.Request
}

// RegisterDictationNames adds a resolver of the names a speaker on a page is
// likely to say: the agent the page talks to, most of all, whose name a
// transcriber otherwise hears as some ordinary word. An app registers one for
// its own pages and returns nothing for a page it does not know. The names
// go to the transcriber only, as its prompt; they are never sent back.
func RegisterDictationNames(fn func(DictationPage) []string) {
	if fn == nil {
		return
	}
	dictationNames.mu.Lock()
	dictationNames.fns = append(dictationNames.fns, fn)
	dictationNames.mu.Unlock()
}

var dictationNames struct {
	mu  sync.Mutex
	fns []func(DictationPage) []string
}

// dictationPageOf reads the page a recording came from out of the headers the
// page sends: X-Dictation-Page (its path) and X-Dictation-Fields (a JSON
// object), each URI-encoded since a header carries only Latin-1. Only string
// values are kept; anything unreadable is left out.
func dictationPageOf(r *http.Request, user string) DictationPage {
	p := DictationPage{User: user, Request: r, Fields: map[string]string{}}
	if v, err := url.QueryUnescape(r.Header.Get("X-Dictation-Page")); err == nil && len(v) <= 512 {
		p.Path = v
	}
	raw, err := url.QueryUnescape(r.Header.Get("X-Dictation-Fields"))
	if err != nil || raw == "" || len(raw) > 4096 {
		return p
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return p
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			p.Fields[k] = s
		}
	}
	return p
}

// dictationPrompt is the transcriber's prompt for a page: the names its
// resolvers give (NamesPrompt). "" when none know the page.
func dictationPrompt(p DictationPage) string {
	dictationNames.mu.Lock()
	fns := append([]func(DictationPage) []string(nil), dictationNames.fns...)
	dictationNames.mu.Unlock()
	var names []string
	for _, fn := range fns {
		names = append(names, fn(p)...)
	}
	return NamesPrompt(names...)
}

// NamesPrompt is a transcriber prompt naming who the speaker is likely to
// say: each name once, in a sentence, since Whisper reads its prompt as text
// spoken before the clip. "" for no names. For WithTranscribePrompt.
func NamesPrompt(names ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.Join(strings.Fields(n), " ")
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return ""
	}
	return "Talking with " + strings.Join(out, ", ") + "."
}

// A clip goes to the transcriber only if it is dictateMinSecs long and, where
// its samples can be read, has dictateMinVoicedMs of 50ms windows louder
// than dictateVoiceLevel: the page meter's tenth of a second of voice, at
// -40 dBFS, half its bar, so a quiet speaker is not taken for silence. The
// bar is fixed, not set against the clip's quietest part as the page's is:
// a clip of talk with no pause in it has no quiet part to set it by.
const (
	dictateMinSecs     = 0.5
	dictateMinVoicedMs = 100
	dictateVoiceLevel  = 0.01
)

// pcmClip is a WAV clip as its header describes it, and its samples.
type pcmClip struct {
	format, channels, bits uint16
	rate, byteRate         uint32
	data                   []byte
}

// readWAV reads a WAV clip's header. ok is false for anything it cannot
// read as one. A data size past the end, as a streamed WAV writes, takes
// what is there.
func readWAV(b []byte) (c pcmClip, ok bool) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return c, false
	}
	le := binary.LittleEndian
	for i := 12; i+8 <= len(b); {
		id, size, body := string(b[i:i+4]), le.Uint32(b[i+4:i+8]), i+8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return c, false
			}
			c.format, c.channels = le.Uint16(b[body:]), le.Uint16(b[body+2:])
			c.rate, c.byteRate = le.Uint32(b[body+4:]), le.Uint32(b[body+8:])
			c.bits = le.Uint16(b[body+14:])
		case "data":
			if c.byteRate == 0 {
				return c, false
			}
			n := uint64(len(b) - body)
			if uint64(size) < n {
				n = uint64(size)
			}
			c.data = b[body : body+int(n)]
			return c, true
		}
		next := uint64(body) + uint64(size) + uint64(size&1)
		if next > uint64(len(b)) {
			return c, false
		}
		i = int(next)
	}
	return c, false
}

// seconds is how long the clip plays.
func (c pcmClip) seconds() float64 { return float64(len(c.data)) / float64(c.byteRate) }

// voice is how much of the clip is louder than dictateVoiceLevel, in 50ms
// windows, and the loudest window's level (RMS, 1 full scale). ok is false
// where the samples are not 16-bit PCM, which it does not read.
func (c pcmClip) voice() (ms int, peak float64, ok bool) {
	if c.format != 1 || c.bits != 16 || c.channels == 0 || c.rate < 20 {
		return 0, 0, false
	}
	win := int(c.rate/20) * int(c.channels) * 2
	for i := 0; i+win <= len(c.data); i += win {
		var sum float64
		for j := i; j < i+win; j += 2 {
			x := float64(int16(binary.LittleEndian.Uint16(c.data[j:]))) / 32768
			sum += x * x
		}
		v := math.Sqrt(sum / float64(win/2))
		if v > peak {
			peak = v
		}
		if v > dictateVoiceLevel {
			ms += 50
		}
	}
	return ms, peak, true
}

// dbfs is a level in decibels below full scale, for the log.
func dbfs(v float64) float64 {
	if v <= 0 {
		return -120
	}
	return 20 * math.Log10(v)
}

// dictationMeterNote is the page's account of what its level meter heard
// (live=1 voiced=150 first=40 peak=0.310), kept to the characters that note
// uses and a line's length, since the page writes it and the log shows it.
func dictationMeterNote(h string) string {
	var b strings.Builder
	for _, c := range h {
		if b.Len() >= 80 {
			break
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("=.- ", c) {
			b.WriteRune(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// dictationFileName names a recording by the format the browser made it in,
// for a transcriber that picks its decoder by extension.
func dictationFileName(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "webm"):
		return "dictation.webm"
	case strings.Contains(ct, "ogg"):
		return "dictation.ogg"
	case strings.Contains(ct, "mp4"), strings.Contains(ct, "aac"), strings.Contains(ct, "m4a"):
		return "dictation.m4a"
	case strings.Contains(ct, "wav"):
		return "dictation.wav"
	}
	return "dictation.webm"
}

// DictationScript is the `<script>` that puts 🎤 in the web chat's input row,
// for pages to embed beside ReadAloudScript. Empty while transcription is off.
//
// Press 🎤 to record (it shows the time), press again to stop; the recording
// is written out by the deployment's transcription, with a spinner meanwhile
// (press it to cancel), and lands in the input box. It stops itself at five
// minutes. A recording whose writing-out failed in a way a retry could fix
// is kept, so the next press sends it again rather than recording anew,
// with a ✕ beside the mic to discard it. With read-aloud on (below) it is
// then sent at once, hands-free; with it off it waits in the box to be read
// over and sent.
//
// With speech on too, a 🔈 beside it says whether the replies to a dictated
// message are read aloud as they arrive, the agent's word before its tools
// run as well as its answer: on by default on a phone, off on a
// desktop, switched per device. The phone's audio is unlocked on the 🎤 tap,
// since a phone plays sound only from a tap and the reply comes seconds after
// it. Sending a message stops a reply being read; typing one does not.
func DictationScript() string {
	if !GetTranscribeConfig().Enabled {
		return ""
	}
	return "<script>" + dictationJS + "</script>"
}

// dictationJS is DictationScript's body, a variable of its own so a test can
// run it.
const dictationJS = `(function(){
  var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏';
  var micGlyph = '🎤', stopGlyph = '⏹', failGlyph = '⚠';
  var speakerOn = '🔈', speakerOff = '🔇';
  var maxMs = 300000;
  // Stopping on silence: once somebody has spoken, this long quiet ends the
  // recording, as a press would. Before anything is heard it waits quietMs,
  // then cancels, so a forgotten tap does not record for five minutes.
  var silenceMs = 1800, quietMs = 15000, tickMs = 50, armMs = 150;
  // The start tone's length; recording begins after it, so it is not heard.
  var toneMs = 150;
  // A stop this soon after the start is a tap on and off, not a recording:
  // it cancels whatever the meter made of it, since a clip that short with
  // nothing said comes back as words the transcriber made up.
  var tapMs = 600;
  var key = 'gohort.readDictatedReplies';

  function readRepliesAloud() {
    var v = null;
    try { v = window.localStorage.getItem(key); } catch (e) {}
    if (v === 'on') return true;
    if (v === 'off') return false;
    return !!(window.matchMedia && window.matchMedia('(pointer: coarse)').matches);
  }
  function setReadRepliesAloud(on) {
    try { window.localStorage.setItem(key, on ? 'on' : 'off'); } catch (e) {}
  }

  // A tenth of a second of silence, to play on the tap that unlocks the
  // phone's audio for the reply that comes later.
  function silentWav() {
    var n = 800, b = new Uint8Array(44 + n), v = new DataView(b.buffer);
    function str(o, s) { for (var i = 0; i < s.length; i++) b[o + i] = s.charCodeAt(i); }
    str(0, 'RIFF'); v.setUint32(4, 36 + n, true); str(8, 'WAVE'); str(12, 'fmt ');
    v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
    v.setUint32(24, 8000, true); v.setUint32(28, 8000, true); v.setUint16(32, 1, true);
    v.setUint16(34, 8, true); str(36, 'data'); v.setUint32(40, n, true);
    for (var i = 0; i < n; i++) b[44 + i] = 128;
    var s = '';
    for (var j = 0; j < b.length; j++) s += String.fromCharCode(b[j]);
    return 'data:audio/wav;base64,' + btoa(s);
  }
  var player = null;
  function unlock() {
    if (!window.gohortReadAloud) return;
    if (!player) player = new Audio();
    player.src = silentWav();
    var p = player.play();
    if (p && p.catch) p.catch(function() {});
  }

  // The audio context for the tones and the level meter, made fresh on each
  // tap: a phone lets a page start sound only from one. One kept for the
  // page's life could stop hearing the microphone (a headset switching, a
  // WebKit context gone quiet) while still saying it was running, and every
  // meter after read nothing but zeros until a reload, so no recording
  // stopped on silence. The last one is closed, since a browser allows only
  // a few at once.
  var audio = null, meter = null;
  function wakeAudio() {
    var AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return;
    if (audio && audio.close) {
      try { var c = audio.close(); if (c && c.catch) c.catch(function() {}); } catch (e) {}
    }
    try {
      audio = new AC();
      if (audio.state === 'suspended' && audio.resume) audio.resume();
    } catch (e) { audio = null; }
  }
  // A short blip: rising when the microphone opens, falling when it closes,
  // so a person not looking at the screen knows when to talk and when it
  // has stopped listening.
  function tone(rising) {
    if (!audio) return;
    try {
      var o = audio.createOscillator(), g = audio.createGain(), t = audio.currentTime;
      o.type = 'sine';
      o.frequency.setValueAtTime(rising ? 660 : 880, t);
      o.frequency.linearRampToValueAtTime(rising ? 990 : 550, t + 0.12);
      g.gain.setValueAtTime(0.0001, t);
      g.gain.exponentialRampToValueAtTime(0.2, t + 0.02);
      g.gain.exponentialRampToValueAtTime(0.0001, t + 0.14);
      o.connect(g); g.connect(audio.destination);
      o.start(t); o.stop(t + 0.15);
    } catch (e) {}
  }

  var state = 'idle', rec = null, stream = null, timer = null, cutoff = null, ctl = null, started = 0;
  var dictated = false, awaiting = false;
  // meterNote is what the meter made of the recording being sent, for the
  // server's log beside it.
  var meterNote = '';
  // held is the recording whose writing-out failed in a way that sending it
  // again could fix ({blob, secs}), and discard the ✕ beside the mic shown
  // while one is. It lives in this page only: a reload or leaving the page
  // lets it go, and so does a message going out without it.
  var held = null, micCtx = null, discard = null;

  function show(ctx, text, title) { ctx.button.textContent = text; ctx.button.title = title; }
  // pressed marks the button as recording, which the composer draws red,
  // and level rings it with how loud the microphone is.
  function pressed(ctx, on) {
    var b = ctx.button;
    if (b.setAttribute) { if (on) b.setAttribute('aria-pressed', 'true'); else b.removeAttribute('aria-pressed'); }
    if (!on) level(ctx, 0);
  }
  function level(ctx, v) {
    var st = ctx.button.style;
    if (st && st.setProperty) st.setProperty('--ui-act-level', v.toFixed(2));
  }
  function stopMeter() {
    if (!meter) return;
    clearInterval(meter.timer);
    try { meter.source.disconnect(); } catch (e) {}
    meter = null;
  }
  function release() {
    stopMeter();
    if (timer) { clearInterval(timer); timer = null; }
    if (cutoff) { clearTimeout(cutoff); cutoff = null; }
    if (stream) { stream.getTracks().forEach(function(t) { t.stop(); }); stream = null; }
    rec = null; ctl = null;
  }
  function idle(ctx) { release(); state = 'idle'; show(ctx, micGlyph, 'Dictate'); }
  function failed(ctx, why) {
    drop();
    release(); state = 'idle';
    show(ctx, failGlyph, 'Dictation failed: ' + String(why || 'unknown error').trim() + ' Press to try again.');
  }
  function clock(secs) { return Math.floor(secs / 60) + ':' + ('0' + secs % 60).slice(-2); }
  // keep holds a recording that failed for a reason a retry could fix: the
  // mic then offers to send it again, and the ✕ beside it to let it go.
  function keep(ctx, blob, secs, why) {
    held = {blob: blob, secs: secs};
    release(); state = 'idle';
    show(ctx, '↻ ' + clock(secs), 'Dictation failed: ' + String(why || 'unknown error').trim() +
      ' Press to send the ' + clock(secs) + ' recording again, or ✕ to discard it.');
    if (discard) discard.button.style.display = '';
  }
  function drop() {
    held = null;
    if (discard) discard.button.style.display = 'none';
  }
  // letGo discards a held recording, stopping a resend of it in flight,
  // and puts the mic back to recording anew.
  function letGo() {
    if (!held) return;
    if (state === 'sending' && ctl) ctl.abort();
    drop();
    if (micCtx) idle(micCtx);
  }
  // retryable says whether sending the same recording again could go
  // differently: no answer at all (the network dropped), too busy, or the
  // transcriber failing or timing out. Not a request refused for what it is
  // (too large, empty, signed out) or transcription being off (503).
  function retryable(e) {
    var st = e && e.status;
    return st == null || st === 429 || (st >= 500 && st !== 503);
  }

  function record(ctx) {
    if (!window.navigator || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || typeof MediaRecorder === 'undefined') {
      failed(ctx, 'this browser cannot record here. The microphone needs a secure (https) page the browser trusts.');
      return;
    }
    unlock();
    wakeAudio();
    // A reply being read stops, and so does the rest of it: a reply still
    // streaming would otherwise read its next piece into the recording. What
    // was not read stays in the thread with its Read aloud button.
    readQueue = [];
    awaiting = false;
    if (window.gohortReadAloudStop) window.gohortReadAloudStop();
    state = 'starting';
    show(ctx, micGlyph, 'Starting the microphone. Press to cancel');
    navigator.mediaDevices.getUserMedia({audio: true}).then(function(s) {
      if (state !== 'starting') { s.getTracks().forEach(function(t) { t.stop(); }); return; }
      stream = s;
      tone(true);
      if (audio) setTimeout(function() { begin(ctx, s); }, toneMs); else begin(ctx, s);
    }).catch(function(e) {
      if (state !== 'starting') return;
      failed(ctx, e && e.name === 'NotAllowedError' ? 'the microphone was not allowed.' : (e && e.message) || e);
    });
  }

  // begin starts recording once the start tone has played, unless a press
  // during the tone cancelled it, which let the stream go.
  function begin(ctx, s) {
    if (stream !== s) return;
    var chunks = [];
    var r = rec = new MediaRecorder(s);
    r.ondataavailable = function(e) { if (e.data && e.data.size) chunks.push(e.data); };
    r.onstop = function() {
      if (state !== 'sending') return;
      transcribe(ctx, new Blob(chunks, {type: r.mimeType || 'audio/webm'}), Math.round((Date.now() - started) / 1000));
    };
    r.start();
    state = 'recording';
    started = Date.now();
    pressed(ctx, true);
    show(ctx, stopGlyph + ' 0:00', audio
      ? 'Recording. It stops when you go quiet, or press to stop now'
      : 'Recording. Press to stop and write it out');
    timer = setInterval(function() {
      var secs = Math.floor((Date.now() - started) / 1000);
      ctx.button.textContent = stopGlyph + ' ' + clock(secs);
    }, 250);
    cutoff = setTimeout(function() { stopRecording(ctx); }, maxMs);
    listen(ctx, s);
  }

  // listen watches the microphone's level and stops the recording when the
  // speaker goes quiet. Quiet is relative to the room: the floor is the
  // lowest level heard, creeping up slowly so a noisier room raises it, and
  // speech is anything well above it. Speech for armMs counts as having said
  // something; before that, only quietMs stops it. A quiet "yes" measured
  // 200ms, under the third of a second this once was, so it waited out the
  // long quiet instead of stopping when the speaker did.
  function listen(ctx, s) {
    if (!audio || !audio.createMediaStreamSource || !audio.createAnalyser) return;
    var source, an;
    try {
      source = audio.createMediaStreamSource(s);
      an = audio.createAnalyser();
      an.fftSize = 1024;
      source.connect(an);
    } catch (e) { return; }
    // Opening the microphone can leave a WebKit context interrupted rather
    // than suspended, where the meter reads nothing but zeros.
    if (audio.state !== 'running' && audio.resume) { try { audio.resume(); } catch (e) {} }
    var buf = new Uint8Array(an.fftSize);
    var fbuf = an.getFloatTimeDomainData ? new Float32Array(an.fftSize) : null;
    var floor = -1, lastVoice = Date.now(), from = Date.now();
    var m = meter = {source: source, voiced: 0, live: false, first: -1, peak: 0, timer: setInterval(function() {
      if (state !== 'recording') return;
      if (!m.live && fbuf) {
        an.getFloatTimeDomainData(fbuf);
        for (var j = 0; j < fbuf.length; j++) if (fbuf[j] !== 0) { m.live = true; break; }
      }
      an.getByteTimeDomainData(buf);
      var sum = 0;
      for (var i = 0; i < buf.length; i++) { var x = (buf[i] - 128) / 128; sum += x * x; }
      var v = Math.sqrt(sum / buf.length);
      floor = (floor < 0 || v < floor) ? v : floor + (v - floor) * 0.005;
      var now = Date.now();
      if (v > m.peak) m.peak = v;
      if (v > Math.max(floor * 4, 0.02)) {
        if (m.first < 0) m.first = now - from;
        m.voiced += tickMs; lastVoice = now;
      }
      level(ctx, Math.min(1, v / 0.15));
      if (m.voiced >= armMs) { if (now - lastVoice >= silenceMs) stopRecording(ctx); }
      else if (now - from >= quietMs) { if (heardNothing()) cancelRecording(ctx); else stopRecording(ctx); }
    }, tickMs)};
  }

  // heardNothing reports whether the meter is listening and has heard no
  // one speak yet. The bar is a tenth of a second of voice, under the armMs
  // the silence stop waits for: a short "yes" must not be thrown
  // away, and a recording is cancelled only when it is truly silent.
  // Without a meter nobody knows, so the recording is sent; so is one whose
  // meter never got a signal (every sample exactly zero, where any real
  // microphone hisses a little), as a web view can give it.
  function heardNothing() { return !!meter && meter.live && meter.voiced < 100; }

  // cancelRecording ends a recording in which nobody was heard: there is
  // nothing to write out, so nothing is sent, and the mic says so. A silent
  // clip sent anyway came back as words the transcriber made up.
  function cancelRecording(ctx) {
    var r = rec;
    state = 'idle';
    pressed(ctx, false);
    tone(false);
    if (r) r.stop();
    idle(ctx);
    ctx.button.title = 'Nothing was heard, so nothing was sent. Press to dictate';
  }

  function stopRecording(ctx) {
    if (state !== 'recording' || !rec) return;
    state = 'sending';
    meterNote = (meter ? 'live=' + (meter.live ? 1 : 0) + ' voiced=' + meter.voiced + ' first=' + meter.first +
      ' peak=' + meter.peak.toFixed(3) + ' ctx=' + audio.state : 'none') + ' ms=' + (Date.now() - started);
    stopMeter();
    pressed(ctx, false);
    tone(false);
    if (timer) { clearInterval(timer); timer = null; }
    if (cutoff) { clearTimeout(cutoff); cutoff = null; }
    writing(ctx);
    var r = rec;
    r.stop();
    if (stream) { stream.getTracks().forEach(function(t) { t.stop(); }); stream = null; }
  }

  // writing spins the button while a recording is written out, from the
  // moment it stops: the browser hands the audio over a beat after, and the
  // recording clock frozen until then read as Stop not having worked.
  function writing(ctx) {
    var from = Date.now(), frame = 0;
    show(ctx, frames.charAt(0), 'Writing it out. Press to cancel');
    timer = setInterval(function() {
      var secs = Math.round((Date.now() - from) / 1000);
      ctx.button.textContent = frames.charAt(frame++ % frames.length) + (secs >= 3 ? ' ' + secs + 's' : '');
    }, 100);
  }

  function transcribe(ctx, blob, secs) {
    state = 'sending';
    ctl = new AbortController();
    if (!timer) writing(ctx);
    var headers = {'Content-Type': blob.type || 'audio/webm'};
    if (meterNote) headers['X-Dictation-Meter'] = meterNote;
    meterNote = '';
    // Which page, and what its next send would carry, so the server can tell
    // the transcriber whose name to expect (RegisterDictationNames).
    // URI-encoded: a header takes only Latin-1, and a name may not be.
    headers['X-Dictation-Page'] = encodeURIComponent((window.location && window.location.pathname) || '');
    try { headers['X-Dictation-Fields'] = encodeURIComponent(JSON.stringify(ctx.fields ? ctx.fields() : {})); } catch (e) {}
    fetch('` + DictatePath + `', {method: 'POST', headers: headers, body: blob, signal: ctl.signal})
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { var e = new Error(t || ('HTTP ' + r.status)); e.status = r.status; throw e; });
        return r.json();
      })
      .then(function(d) {
        if (state !== 'sending') return;
        var text = String(d && d.text || '').trim();
        if (!text) { failed(ctx, 'nothing was heard in the recording.'); return; }
        drop();
        ctx.insertText(text);
        dictated = true;
        markSpoken();
        idle(ctx);
        // Talking with read-aloud on is hands-free: it goes at once, and a
        // misheard word is fixed with the message's Edit. With read-aloud off
        // the reply will be read, so the words wait in the box to be looked
        // over first.
        if (window.gohortReadAloud && readRepliesAloud() && ctx.send) ctx.send();
      })
      .catch(function(e) {
        if (e && e.name === 'AbortError') return;
        if (state !== 'sending') return;
        if (retryable(e)) keep(ctx, blob, secs, e && e.message || e);
        else failed(ctx, e && e.message || e);
      });
  }

  function press(ctx) {
    switch (state) {
    case 'recording':
      if (Date.now() - started < tapMs || heardNothing()) cancelRecording(ctx); else stopRecording(ctx);
      return;
    case 'sending':
      if (ctl) ctl.abort();
      // A cancelled resend keeps the recording; a first send lets it go,
      // as cancelling always has.
      if (held) keep(ctx, held.blob, held.secs, 'the resend was cancelled.'); else idle(ctx);
      return;
    case 'starting': idle(ctx); return;
    }
    if (held) { transcribe(ctx, held.blob, held.secs); return; }
    record(ctx);
  }

  // A dictated send tells the agent so (dictated on the send body), since
  // speech-to-text mishears a word now and then, a name most of all. One
  // whose reply will be read aloud says that too, and it answers the way it
  // would speak (spoken_reply). The panel clears both after one send;
  // switching read-aloud before sending updates it.
  function markSpoken() {
    if (dictated && window.uiSetPendingMessageExtras) {
      window.uiSetPendingMessageExtras({dictated: true, spoken_reply: !!window.gohortReadAloud && readRepliesAloud()});
    }
  }

  // The replies to a dictated message are read aloud as each one is written,
  // in order: the agent's word before it works ("let me look that up") plays
  // while its tools run, and the answer follows it. One piece at a time,
  // since starting a read stops the one playing; the next goes only once
  // the last has played to its end, and stopping one (a send, its button,
  // another reply pressed) drops the rest.
  var readQueue = [], reading = false;
  function readLater(bubble, text) {
    readQueue.push({bubble: bubble, text: text});
    // After this tick, so the bubble's own Read aloud button is drawn and
    // shows the piece playing.
    setTimeout(readNext, 0);
  }
  function readNext() {
    if (reading || !readQueue.length) return;
    // Asked at play time, so switching it off drops what is waiting too.
    if (!window.gohortReadAloud || !readRepliesAloud()) { readQueue = []; return; }
    var m = readQueue.shift();
    var btn = m.bubble && m.bubble.querySelector ? m.bubble.querySelector('button[title="Read aloud"]') : null;
    reading = true;
    window.gohortReadAloud({bubble: m.bubble, getText: function() { return m.text; },
      agentId: window.GOHORT_AGENT_ID || '', audio: player, button: btn,
      onDone: function(ended) {
        reading = false;
        if (ended) readNext(); else readQueue = [];
      }});
  }
  window.addEventListener('ui-agent-sent', function() {
    // A message going out while a recording is held was written another
    // way, so the recording is out of date.
    letGo();
    // So is the reply being read: the person has moved on. Typing leaves it
    // playing, since they may be listening while they write.
    readQueue = [];
    if (window.gohortReadAloudStop) window.gohortReadAloudStop();
    awaiting = dictated;
    dictated = false;
  });
  // A result posted later, for a request made by voice (a background task's
  // answer, marked spoken), is read aloud as it arrives, but only to someone
  // who can be listening: the page in view, read-aloud on for this device,
  // and the mic not in use. Otherwise it waits in the thread with its Read
  // aloud button, rather than a tab starting to talk minutes later.
  window.addEventListener('ui-agent-message-arrived', function(e) {
    var d = e && e.detail, m = d && d.message;
    if (!m || !m.spoken || m.role !== 'assistant' || !String(m.content || '').trim()) return;
    if (!window.gohortReadAloud || !readRepliesAloud() || state !== 'idle') return;
    if (typeof document !== 'undefined' && document.visibilityState && document.visibilityState !== 'visible') return;
    readLater(d.bubble, m.content);
  });
  window.addEventListener('ui-agent-turn-done', function() { awaiting = false; });

  function showSpeaker(ctx) {
    var on = readRepliesAloud();
    show(ctx, on ? speakerOn : speakerOff, on
      ? 'Replies to dictated messages are read aloud on this device. Press to stop that.'
      : 'Replies to dictated messages are not read aloud on this device. Press to read them.');
  }
  function register() {
    if (!window.uiRegisterComposerAction || !window.uiRegisterMessageDecorator) { setTimeout(register, 50); return; }
    window.uiRegisterMessageDecorator(function(m) {
      if (!awaiting || !m || m.role !== 'assistant' || !String(m.rawText || '').trim()) return;
      readLater(m.wrap, m.rawText);
    });
    // First in the row, the mic then its switch: the button most reached for
    // sits in the corner under a phone user's thumb, and is drawn prominent,
    // larger on a touch screen than the buttons beside it.
    var mic = micCtx = window.uiRegisterComposerAction({label: micGlyph, title: 'Dictate', first: true, prominent: true, onclick: press});
    // Registered next, so it sits right beside the mic, and shown only while
    // a failed recording is held.
    discard = window.uiRegisterComposerAction({label: '✕', title: 'Discard the recording', first: true, onclick: letGo});
    if (discard) discard.button.style.display = 'none';
    if (window.gohortReadAloud) {
      var speaker = window.uiRegisterComposerAction({label: speakerOn, first: true, onclick: function(ctx) {
        setReadRepliesAloud(!readRepliesAloud());
        showSpeaker(ctx);
        markSpoken();
      }});
      if (speaker) showSpeaker(speaker);
    }
  }
  register();
})();`
