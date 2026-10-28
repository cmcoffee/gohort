// Dictation: the web chat's 🎤 (DictationScript). The page records a clip with
// the browser's own recorder and posts it here; the deployment's transcription
// turns it into text, which the page puts in the input box to be read over and
// sent. The same Whisper that hears a voice memo hears this, so a recording
// never leaves for a browser vendor's speech service.

package media

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

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
	text, err := Transcribe(r.Context(), send, name)
	if err != nil {
		if r.Context().Err() == nil {
			nfo.Log("[dictate] transcription for %s failed: %v", user, err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"text": strings.TrimSpace(text)})
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
// With speech on too, a 🔈 beside it says whether the reply to a dictated
// message is read aloud when it arrives: on by default on a phone, off on a
// desktop, switched per device. The phone's audio is unlocked on the 🎤 tap,
// since a phone plays sound only from a tap and the reply comes seconds after
// it. Typing stops a reply being read.
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
  // then stops, so a forgotten tap does not record for five minutes.
  var silenceMs = 1800, quietMs = 15000, tickMs = 50;
  // The start tone's length; recording begins after it, so it is not heard.
  var toneMs = 150;
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

  // One audio context for the tones and the level meter, made or woken on
  // the tap: a phone lets a page start sound only from one.
  var audio = null, meter = null;
  function wakeAudio() {
    var AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return;
    try {
      if (!audio) audio = new AC();
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
  var dictated = false, awaiting = false, lastReply = null;
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
  // speech is anything well above it. A third of a second of speech counts
  // as having said something; before that, only quietMs stops it.
  function listen(ctx, s) {
    if (!audio || !audio.createMediaStreamSource || !audio.createAnalyser) return;
    var source, an;
    try {
      source = audio.createMediaStreamSource(s);
      an = audio.createAnalyser();
      an.fftSize = 1024;
      source.connect(an);
    } catch (e) { return; }
    var buf = new Uint8Array(an.fftSize);
    var floor = -1, voiced = 0, lastVoice = Date.now(), from = Date.now();
    meter = {source: source, timer: setInterval(function() {
      if (state !== 'recording') return;
      an.getByteTimeDomainData(buf);
      var sum = 0;
      for (var i = 0; i < buf.length; i++) { var x = (buf[i] - 128) / 128; sum += x * x; }
      var v = Math.sqrt(sum / buf.length);
      floor = (floor < 0 || v < floor) ? v : floor + (v - floor) * 0.005;
      var now = Date.now();
      if (v > Math.max(floor * 4, 0.02)) { voiced += tickMs; lastVoice = now; }
      level(ctx, Math.min(1, v / 0.15));
      if (voiced >= 300 ? now - lastVoice >= silenceMs : now - from >= quietMs) stopRecording(ctx);
    }, tickMs)};
  }

  function stopRecording(ctx) {
    if (state !== 'recording' || !rec) return;
    state = 'sending';
    stopMeter();
    pressed(ctx, false);
    tone(false);
    if (timer) { clearInterval(timer); timer = null; }
    if (cutoff) { clearTimeout(cutoff); cutoff = null; }
    var r = rec;
    r.stop();
    if (stream) { stream.getTracks().forEach(function(t) { t.stop(); }); stream = null; }
  }

  function transcribe(ctx, blob, secs) {
    state = 'sending';
    ctl = new AbortController();
    started = Date.now();
    var frame = 0;
    show(ctx, frames.charAt(0), 'Writing it out. Press to cancel');
    timer = setInterval(function() {
      var secs = Math.round((Date.now() - started) / 1000);
      ctx.button.textContent = frames.charAt(frame++ % frames.length) + (secs >= 3 ? ' ' + secs + 's' : '');
    }, 100);
    fetch('` + DictatePath + `', {method: 'POST', headers: {'Content-Type': blob.type || 'audio/webm'}, body: blob, signal: ctl.signal})
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
    case 'recording': stopRecording(ctx); return;
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

  // A send whose reply will be read aloud tells the agent so, and it answers
  // the way it would speak (spoken_reply on the send body; the panel clears
  // it after one send). Switching read-aloud before sending updates it.
  function markSpoken() {
    if (dictated && window.uiSetPendingMessageExtras) {
      window.uiSetPendingMessageExtras({spoken_reply: !!window.gohortReadAloud && readRepliesAloud()});
    }
  }

  // The reply to a dictated message, read aloud once the turn is over: the
  // last reply of the turn, so a note before the answer is not what is read.
  window.addEventListener('ui-agent-sent', function() {
    // A message going out while a recording is held was written another
    // way, so the recording is out of date.
    letGo();
    awaiting = dictated;
    dictated = false;
    lastReply = null;
  });
  window.addEventListener('ui-agent-turn-done', function() {
    if (!awaiting) return;
    awaiting = false;
    var m = lastReply;
    lastReply = null;
    if (!m || !window.gohortReadAloud || !readRepliesAloud()) return;
    var btn = m.wrap && m.wrap.querySelector ? m.wrap.querySelector('button[title="Read aloud"]') : null;
    window.gohortReadAloud({bubble: m.wrap, getText: function() { return m.rawText; },
      agentId: window.GOHORT_AGENT_ID || '', audio: player, button: btn});
  });

  function showSpeaker(ctx) {
    var on = readRepliesAloud();
    show(ctx, on ? speakerOn : speakerOff, on
      ? 'Replies to dictated messages are read aloud on this device. Press to stop that.'
      : 'Replies to dictated messages are not read aloud on this device. Press to read them.');
  }
  function register() {
    if (!window.uiRegisterComposerAction || !window.uiRegisterMessageDecorator) { setTimeout(register, 50); return; }
    window.uiRegisterMessageDecorator(function(m) {
      if (awaiting && m && m.role === 'assistant' && String(m.rawText || '').trim()) lastReply = m;
    });
    // First in the row, the mic then its switch: the button most reached for
    // sits in the corner under a phone user's thumb, and is drawn prominent,
    // larger on a touch screen than the buttons beside it.
    var mic = micCtx = window.uiRegisterComposerAction({label: micGlyph, title: 'Dictate', first: true, prominent: true, onclick: press});
    // Registered next, so it sits right beside the mic, and shown only while
    // a failed recording is held.
    discard = window.uiRegisterComposerAction({label: '✕', title: 'Discard the recording', first: true, onclick: letGo});
    if (discard) discard.button.style.display = 'none';
    if (mic && mic.input) {
      mic.input.addEventListener('input', function() { if (window.gohortReadAloudStop) window.gohortReadAloudStop(); });
    }
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
