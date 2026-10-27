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

// dictateMaxBytes bounds one recording: the page stops itself at two
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
		http.Error(w, "the recording is too large: dictation takes up to two minutes", http.StatusRequestEntityTooLarge)
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
// (press it to cancel), and lands in the input box. It stops itself at two
// minutes. With read-aloud on (below) it is then sent at once, hands-free;
// with it off it waits in the box to be read over and sent.
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
  var maxMs = 120000;
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

  var state = 'idle', rec = null, stream = null, timer = null, cutoff = null, ctl = null, started = 0;
  var dictated = false, awaiting = false, lastReply = null;

  function show(ctx, text, title) { ctx.button.textContent = text; ctx.button.title = title; }
  function release() {
    if (timer) { clearInterval(timer); timer = null; }
    if (cutoff) { clearTimeout(cutoff); cutoff = null; }
    if (stream) { stream.getTracks().forEach(function(t) { t.stop(); }); stream = null; }
    rec = null; ctl = null;
  }
  function idle(ctx) { release(); state = 'idle'; show(ctx, micGlyph, 'Dictate'); }
  function failed(ctx, why) {
    release(); state = 'idle';
    show(ctx, failGlyph, 'Dictation failed: ' + String(why || 'unknown error').trim() + ' Press to try again.');
  }

  function record(ctx) {
    if (!window.navigator || !navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || typeof MediaRecorder === 'undefined') {
      failed(ctx, 'this browser cannot record here. The microphone needs a secure (https) page the browser trusts.');
      return;
    }
    unlock();
    if (window.gohortReadAloudStop) window.gohortReadAloudStop();
    state = 'starting';
    show(ctx, micGlyph, 'Starting the microphone. Press to cancel');
    navigator.mediaDevices.getUserMedia({audio: true}).then(function(s) {
      if (state !== 'starting') { s.getTracks().forEach(function(t) { t.stop(); }); return; }
      stream = s;
      var chunks = [];
      var r = rec = new MediaRecorder(s);
      r.ondataavailable = function(e) { if (e.data && e.data.size) chunks.push(e.data); };
      r.onstop = function() {
        if (state !== 'sending') return;
        transcribe(ctx, new Blob(chunks, {type: r.mimeType || 'audio/webm'}));
      };
      r.start();
      state = 'recording';
      started = Date.now();
      show(ctx, stopGlyph + ' 0:00', 'Recording. Press to stop and write it out');
      timer = setInterval(function() {
        var secs = Math.floor((Date.now() - started) / 1000);
        ctx.button.textContent = stopGlyph + ' ' + Math.floor(secs / 60) + ':' + ('0' + secs % 60).slice(-2);
      }, 250);
      cutoff = setTimeout(function() { stopRecording(ctx); }, maxMs);
    }).catch(function(e) {
      if (state !== 'starting') return;
      failed(ctx, e && e.name === 'NotAllowedError' ? 'the microphone was not allowed.' : (e && e.message) || e);
    });
  }

  function stopRecording(ctx) {
    if (state !== 'recording' || !rec) return;
    state = 'sending';
    if (timer) { clearInterval(timer); timer = null; }
    if (cutoff) { clearTimeout(cutoff); cutoff = null; }
    var r = rec;
    r.stop();
    if (stream) { stream.getTracks().forEach(function(t) { t.stop(); }); stream = null; }
  }

  function transcribe(ctx, blob) {
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
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      })
      .then(function(d) {
        if (state !== 'sending') return;
        var text = String(d && d.text || '').trim();
        if (!text) { failed(ctx, 'nothing was heard in the recording.'); return; }
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
        if (state === 'sending') failed(ctx, e && e.message || e);
      });
  }

  function press(ctx) {
    switch (state) {
    case 'recording': stopRecording(ctx); return;
    case 'sending': if (ctl) ctl.abort(); idle(ctx); return;
    case 'starting': idle(ctx); return;
    }
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
    // sits in the corner under a phone user's thumb.
    var mic = window.uiRegisterComposerAction({label: micGlyph, title: 'Dictate', first: true, onclick: press});
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
