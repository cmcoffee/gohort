package media

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A recording posted by a signed-in user comes back written out; every way it
// cannot says why.
func TestServeDictation(t *testing.T) {
	var got []byte
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		if f, _, err := r.FormFile("file"); err == nil {
			got, _ = io.ReadAll(f)
		}
		w.WriteHeader(status)
		w.Write([]byte("  Meet me at the station at noon.  "))
	}))
	defer srv.Close()
	prevCfg, prevHook, prevUpload := GetTranscribeConfig(), TranscribeHook, GovernedUploadFunc
	TranscribeHook, GovernedUploadFunc = nil, nil
	SetTranscribeConfig(TranscribeConfig{Endpoint: srv.URL, Enabled: true})
	t.Cleanup(func() { SetTranscribeConfig(prevCfg); TranscribeHook, GovernedUploadFunc = prevHook, prevUpload })

	do := func(method, user, ctype string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, DictatePath, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", ctype)
		w := httptest.NewRecorder()
		ServeDictation(w, r, user)
		return w
	}
	w := do("POST", "alice", "audio/webm;codecs=opus", []byte("recorded audio"))
	var out struct{ Text string }
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusOK || out.Text != "Meet me at the station at noon." {
		t.Errorf("a recording written out: %d %q", w.Code, w.Body.String())
	}
	if len(got) == 0 {
		t.Error("the recording should reach the transcriber")
	}

	cases := []struct {
		name, method, user string
		body               []byte
		code               int
	}{
		{"signed out", "POST", "", []byte("x"), http.StatusForbidden},
		{"wrong method", "GET", "alice", nil, http.StatusMethodNotAllowed},
		{"empty", "POST", "alice", nil, http.StatusBadRequest},
		{"too long", "POST", "alice", make([]byte, dictateMaxBytes+1), http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		if w := do(c.method, c.user, "audio/webm", c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.code)
		}
	}
	status = http.StatusInternalServerError
	if w := do("POST", "alice", "audio/webm", []byte("x")); w.Code != http.StatusBadGateway {
		t.Errorf("the transcriber failing: %d", w.Code)
	}
	SetTranscribeConfig(TranscribeConfig{Endpoint: srv.URL, Enabled: false})
	if w := do("POST", "alice", "audio/webm", []byte("x")); w.Code != http.StatusServiceUnavailable {
		t.Errorf("transcription off: %d", w.Code)
	}
}

func TestDictationFileName(t *testing.T) {
	for ct, want := range map[string]string{
		"audio/webm;codecs=opus": "dictation.webm",
		"audio/mp4":              "dictation.m4a",
		"audio/ogg; codecs=opus": "dictation.ogg",
		"audio/wav":              "dictation.wav",
		"":                       "dictation.webm",
	} {
		if got := dictationFileName(ct); got != want {
			t.Errorf("dictationFileName(%q) = %q, want %q", ct, got, want)
		}
	}
}

// The mic button only exists where the recording can be written out.
func TestDictationScriptOnlyWhileTranscriptionIsOn(t *testing.T) {
	prev := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(prev) })
	SetTranscribeConfig(TranscribeConfig{Endpoint: "http://x", Enabled: false})
	if s := DictationScript(); s != "" {
		t.Errorf("transcription off should add nothing: %.80q", s)
	}
	SetTranscribeConfig(TranscribeConfig{Endpoint: "http://x", Enabled: true})
	if s := DictationScript(); !strings.HasPrefix(s, "<script>") || !strings.Contains(s, "'"+DictatePath+"'") {
		t.Errorf("transcription on should add the script posting to %s: %.120q", DictatePath, s)
	}
}

// dictationHarness drives dictationJS the way the chat does: the panel's
// composer and decorator registries, its sent and turn-done events, a
// microphone and recorder that do what the test says, read-aloud standing in
// as a recorder of what it was asked to read, and per-device storage.
const dictationHarness = dictationSetup + `
eval(dictationJS);
` + dictationFlow

// dictationSetup is the page the script runs in, for both harnesses.
const dictationSetup = `
var fail = 0;
function check(label, cond) { if (cond) console.log('ok   ' + label); else { fail++; console.log('FAIL ' + label); } }
function sleep(ms) { return new Promise(function(r) { setTimeout(r, ms); }); }
var listeners = {};
var store = {};
var coarse = true;
var actions = [], decorators = [], readCalls = [], stops = 0, extras = [], sends = 0;
var input = {value: '', on: {}, addEventListener: function(t, f) { this.on[t] = f; }};
global.window = {
  addEventListener: function(t, f) { (listeners[t] = listeners[t] || []).push(f); },
  localStorage: {getItem: function(k) { return k in store ? store[k] : null; }, setItem: function(k, v) { store[k] = v; }},
  matchMedia: function(q) { return {matches: coarse && q === '(pointer: coarse)'}; },
  uiRegisterComposerAction: function(o) {
    var ctx = {button: {textContent: o.label, title: o.title || '', attrs: {},
        setAttribute: function(k, v) { this.attrs[k] = v; }, removeAttribute: function(k) { delete this.attrs[k]; },
        style: {props: {}, setProperty: function(k, v) { this.props[k] = v; }}}, input: input,
      insertText: function(t) { input.value += (input.value ? ' ' : '') + t; },
      send: function() { sends++; }, onclick: o.onclick, first: o.first, prominent: o.prominent};
    actions.push(ctx);
    return ctx;
  },
  uiRegisterMessageDecorator: function(f) { decorators.push(f); },
  gohortReadAloud: function(ctx) { readCalls.push(ctx); },
  gohortReadAloudStop: function() { stops++; },
  uiSetPendingMessageExtras: function(o) { extras.push(o); },
  GOHORT_AGENT_ID: 'a1',
};
function fire(t, detail) { (listeners[t] || []).forEach(function(f) { f({detail: detail}); }); }
var mic = null, micDenied = false, tracksStopped = 0;
Object.defineProperty(global, 'navigator', {configurable: true, value: {mediaDevices: {getUserMedia: function() {
  if (micDenied) { var e = new Error('denied'); e.name = 'NotAllowedError'; return Promise.reject(e); }
  return Promise.resolve({getTracks: function() { return [{stop: function() { tracksStopped++; }}]; }});
}}}});
window.navigator = navigator;
var recorders = [];
global.MediaRecorder = function(s) { this.mimeType = 'audio/webm;codecs=opus'; recorders.push(this); };
MediaRecorder.prototype.start = function() { this.started = true; };
// A real browser hands the audio over a beat after stop; with lateStop set
// the fake holds it until handOver() is called.
var lateStop = false, handOver = null;
MediaRecorder.prototype.stop = function() {
  this.stopped = true;
  var r = this;
  var hand = function() {
    r.ondataavailable({data: new Blob(['sound'], {type: 'audio/webm'})});
    r.onstop();
  };
  if (lateStop) handOver = hand; else hand();
};
var played = [];
global.Audio = function() { this.src = ''; };
Audio.prototype.play = function() { played.push(this.src); return Promise.resolve(); };
var pending = [];
global.fetch = function(url, opts) {
  return new Promise(function(res, rej) {
    pending.push({url: url, opts: opts, res: res, rej: rej});
    opts.signal.addEventListener('abort', function() { var e = new Error('aborted'); e.name = 'AbortError'; rej(e); });
  });
};
function written(text) { return {ok: true, json: function() { return Promise.resolve({text: text}); }}; }
function refused(status, text) { return {ok: false, status: status, text: function() { return Promise.resolve(text); }}; }
`

// dictationFlow is the mic with no audio context, as on a browser without
// one: it records until pressed.
const dictationFlow = `
(async function() {
  await sleep(10);
  mic = actions[0];
  var discard = actions[1], speaker = actions[2];
  // Each press comes a second after the one before, as a person's would; a
  // tap on and off is pressed with tap().
  var skew = 0, realNow = Date.now, tap = mic.onclick;
  Date.now = function() { return realNow() + skew; };
  mic.onclick = function(c) { skew += 1000; tap(c); };
  check('a mic button in the input row', mic && mic.button.textContent === '🎤');
  check('a ✕ beside it, hidden until a recording is held', discard && discard.first === true && discard.button.style.display === 'none');
  check('and the read-aloud switch beside it, on by default on a phone', speaker && speaker.button.textContent === '🔈');
  check('both lead the row, under the thumb', mic.first === true && speaker.first === true);
  check('and the mic is drawn prominent', mic.prominent === true && !speaker.prominent);

  mic.onclick(mic);
  await sleep(10);
  check('pressing it records and shows the time', recorders.length === 1 && recorders[0].started && mic.button.textContent.indexOf('⏹ 0:0') === 0);
  check('marked as recording', mic.button.attrs['aria-pressed'] === 'true');
  check('with no audio context it says to press to stop', mic.button.title.indexOf('Press to stop') >= 0 && mic.button.title.indexOf('quiet') < 0);
  check('the tap unlocks the player for the reply', played.length === 1 && played[0].indexOf('data:audio/wav;base64,') === 0);
  check('and stops a reply being read', stops === 1);
  mic.onclick(mic);
  await sleep(10);
  check('pressing again stops and sends the recording', pending.length === 1 && pending[0].url === '/api/dictate' && pending[0].opts.headers['Content-Type'] === 'audio/webm;codecs=opus' && tracksStopped >= 1);
  check('saying it had no meter', pending[0].opts.headers['X-Dictation-Meter'] === 'none');
  check('and it is no longer marked as recording', !('aria-pressed' in mic.button.attrs));
  await sleep(150);
  check('a spinner while it is written out', /^[⠀-⣿]/.test(mic.button.textContent));
  pending[0].res(written('Meet me at noon.'));
  await sleep(10);
  check('the words land in the box to be read over', input.value === 'Meet me at noon.');
  check('and the button is ready again', mic.button.textContent === '🎤');
  check('the send tells the agent its reply will be heard', extras.length === 1 && extras[0].spoken_reply === true);
  check('with read-aloud on it goes at once, hands-free', sends === 1);

  // The replies to the dictated message are read as each is written, in
  // order: the note before the agent's tools run, then its answer, which
  // waits for the note to finish.
  fire('ui-agent-sent');
  var note = {role: 'assistant', rawText: 'Checking.', wrap: {querySelector: function() { return null; }}};
  var answer = {role: 'assistant', rawText: 'Noon works.', wrap: {querySelector: function() { return {title: 'Read aloud'}; }}};
  decorators.forEach(function(d) { d(note); });
  await sleep(5);
  check('the note before the tools run is read as it is written', readCalls.length === 1 && readCalls[0].getText() === 'Checking.' && readCalls[0].bubble === note.wrap);
  decorators.forEach(function(d) { d(answer); });
  await sleep(5);
  check('the answer waits for it to finish', readCalls.length === 1);
  readCalls[0].onDone(true);
  check('and is read once it has', readCalls.length === 2 && readCalls[1].getText() === 'Noon works.' && readCalls[1].bubble === answer.wrap);
  check('in the unlocked player, for the right agent, with its button', readCalls[1].audio && readCalls[1].agentId === 'a1' && readCalls[1].button);
  // Stopping a piece drops the ones waiting: the person stopped listening.
  var more = {role: 'assistant', rawText: 'One more thing.', wrap: {}}, last = {role: 'assistant', rawText: 'Done.', wrap: {}};
  decorators.forEach(function(d) { d(more); d(last); });
  await sleep(5);
  readCalls[1].onDone(false);
  await sleep(5);
  check('stopping a piece drops the ones waiting', readCalls.length === 2);
  fire('ui-agent-turn-done');
  decorators.forEach(function(d) { d(answer); });
  await sleep(5);
  check('a reply after the turn is over is not read', readCalls.length === 2);

  // A typed message's reply is not read.
  fire('ui-agent-sent');
  decorators.forEach(function(d) { d(answer); });
  await sleep(5);
  fire('ui-agent-turn-done');
  check('a typed message is not answered aloud', readCalls.length === 2);

  // A result posted later for a request made by voice is read as it arrives,
  // to someone who can be listening.
  var card = {querySelector: function() { return {title: 'Read aloud'}; }};
  async function arrive(m) { fire('ui-agent-message-arrived', m); await sleep(5); }
  await arrive({message: {role: 'assistant', spoken: true, content: 'The venue is the mill.'}, bubble: card});
  check('a spoken result is read aloud as it arrives', readCalls.length === 3 && readCalls[2].getText() === 'The venue is the mill.' &&
    readCalls[2].bubble === card && readCalls[2].button && readCalls[2].audio && readCalls[2].agentId === 'a1');
  readCalls[2].onDone(true);
  await arrive({message: {role: 'assistant', content: 'A scheduled report.'}, bubble: card});
  check('a result not asked for by voice is not', readCalls.length === 3);
  await arrive({message: {role: 'user', spoken: true, content: 'hello'}, bubble: card});
  await arrive({message: {role: 'assistant', spoken: true, content: '  '}, bubble: card});
  await arrive({});
  check('nor a message that is not a reply with words', readCalls.length === 3);
  global.document = {visibilityState: 'hidden'};
  await arrive({message: {role: 'assistant', spoken: true, content: 'Later.'}, bubble: card});
  check('a page out of view does not start talking', readCalls.length === 3);
  global.document = {visibilityState: 'visible'};
  await arrive({message: {role: 'assistant', spoken: true, content: 'Now.'}, bubble: card});
  check('back in view it does', readCalls.length === 4);
  readCalls[3].onDone(true);
  delete global.document;

  // Switched off on this device, a dictated message's reply is not read.
  speaker.onclick(speaker);
  check('the switch turns it off and says so', store['gohort.readDictatedReplies'] === 'off' && speaker.button.textContent === '🔇');
  check('with nothing dictated waiting, switching tags no send', extras.length === 1);
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[1].res(written('Thanks.'));
  await sleep(10);
  check('switched off, a dictated send is not told its reply will be heard', extras.length === 2 && extras[1].spoken_reply === false);
  check('and the words wait in the box to be read over', sends === 1 && input.value.indexOf('Thanks.') >= 0);
  speaker.onclick(speaker);
  check('switching back on before sending tags it again', extras.length === 3 && extras[2].spoken_reply === true);
  speaker.onclick(speaker);
  var heard = readCalls.length;
  fire('ui-agent-sent');
  decorators.forEach(function(d) { d(answer); });
  await sleep(5);
  fire('ui-agent-turn-done');
  check('switched off, it is not read', readCalls.length === heard);

  // Typing stops a reply being read.
  var before = stops;
  input.on.input();
  check('typing stops a reply being read', stops === before + 1);

  // Cancelling while it is written out.
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  mic.onclick(mic);
  check('pressing during the spinner cancels it', pending[2].opts.signal.aborted && mic.button.textContent === '🎤');
  check('and a first send cancelled is not kept', discard.button.style.display === 'none');

  // Failures say why. One a retry cannot fix lets the recording go.
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[3].res(refused(413, 'the recording is too large'));
  await sleep(10);
  check('a refused recording says why', mic.button.textContent === '⚠' && mic.button.title.indexOf('too large') >= 0);
  check('and is not kept', discard.button.style.display === 'none');
  var recs = recorders.length;
  mic.onclick(mic); await sleep(10);
  check('so the next press records anew', recorders.length === recs + 1);
  mic.onclick(mic); await sleep(10);
  pending[4].res(written('   '));
  await sleep(10);
  check('a silent recording says nothing was heard', mic.button.textContent === '⚠' && mic.button.title.indexOf('nothing was heard') >= 0);
  check('and sends nothing', sends === 1);
  check('and is not kept either', discard.button.style.display === 'none');
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[5].res(refused(503, 'transcription is not set up'));
  await sleep(10);
  check('transcription being off is not kept', mic.button.textContent === '⚠' && discard.button.style.display === 'none');

  // A failure a retry could fix keeps the recording, to send again.
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  var kept = pending[6].opts.body;
  pending[6].res(refused(502, 'transcribe: HTTP 500'));
  await sleep(10);
  check('a transcriber failure says why and keeps the recording', mic.button.textContent === '↻ 0:01' &&
    mic.button.title.indexOf('HTTP 500') >= 0 && mic.button.title.indexOf('send the 0:01 recording again') >= 0);
  check('with the ✕ shown to discard it', discard.button.style.display === '');
  recs = recorders.length;
  mic.onclick(mic); await sleep(10);
  check('pressing sends the same recording again, recording nothing new', pending.length === 8 && pending[7].opts.body === kept && recorders.length === recs);
  check('with the spinner', /^[⠀-⣿]/.test(mic.button.textContent));
  mic.onclick(mic);
  check('cancelling the resend keeps it', pending[7].opts.signal.aborted && mic.button.textContent === '↻ 0:01' && discard.button.style.display === '');
  mic.onclick(mic); await sleep(10);
  pending[8].rej(new TypeError('Failed to fetch'));
  await sleep(10);
  check('a dropped network keeps it too', mic.button.textContent === '↻ 0:01' && mic.button.title.indexOf('Failed to fetch') >= 0);
  mic.onclick(mic); await sleep(10);
  pending[9].res(written('Back online.'));
  await sleep(10);
  check('a resend that works writes it out', input.value.indexOf('Back online.') >= 0 && pending[9].opts.body === kept);
  check('and lets it go', mic.button.textContent === '🎤' && discard.button.style.display === 'none');

  // ✕ lets a held recording go, and so does a message going out without it.
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[10].res(refused(429, 'too many requests'));
  await sleep(10);
  discard.onclick(discard);
  check('✕ discards it, back to a fresh mic', mic.button.textContent === '🎤' && discard.button.style.display === 'none');
  recs = recorders.length;
  mic.onclick(mic); await sleep(10);
  check('so the next press records anew', recorders.length === recs + 1);
  discard.onclick(discard);
  check('a ✕ with nothing held does nothing', mic.button.textContent.indexOf('⏹') === 0);
  mic.onclick(mic); await sleep(10);
  pending[11].res(refused(500, 'busy'));
  await sleep(10);
  fire('ui-agent-sent');
  check('a message going out another way lets it go', mic.button.textContent === '🎤' && discard.button.style.display === 'none');
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[12].res(refused(502, 'busy'));
  await sleep(10);
  mic.onclick(mic); await sleep(10);
  discard.onclick(discard);
  check('✕ during a resend stops it and lets it go', pending[13].opts.signal.aborted && mic.button.textContent === '🎤' && discard.button.style.display === 'none');
  mic.onclick(mic); await sleep(10); mic.onclick(mic); await sleep(10);
  pending[14].res(refused(502, 'busy'));
  await sleep(10);
  mic.onclick(mic); await sleep(10);
  pending[15].res(written(''));
  await sleep(10);
  check('a resend that hears nothing lets it go', mic.button.textContent === '⚠' && discard.button.style.display === 'none');
  recs = recorders.length;
  mic.onclick(mic); await sleep(10);
  check('and the next press records anew', recorders.length === recs + 1);
  mic.onclick(mic); await sleep(10);
  pending[16].res(refused(502, 'busy'));
  await sleep(10);
  mic.onclick(mic); await sleep(10);
  fire('ui-agent-sent');
  check('a message going out during a resend stops it and lets it go', pending[17].opts.signal.aborted && mic.button.textContent === '🎤' && discard.button.style.display === 'none');
  fire('ui-agent-sent');
  var sentBefore = pending.length;
  tap(mic); await sleep(10); tap(mic); await sleep(10);
  check('a tap on and off sends nothing, without a meter to ask', recorders[recorders.length - 1].stopped &&
    pending.length === sentBefore && mic.button.textContent === '🎤' && mic.button.title.indexOf('Nothing was heard') === 0);
  lateStop = true;
  mic.onclick(mic); await sleep(10);
  var sent = pending.length;
  mic.onclick(mic);
  check('Stop spins at once, before the browser hands the audio over', /^[⠀-⣿]/.test(mic.button.textContent) && pending.length === sent);
  handOver(); await sleep(10);
  check('and sends it once it does', pending.length === sent + 1 && /^[⠀-⣿]/.test(mic.button.textContent));
  pending[sent].res(written('ok'));
  await sleep(10);
  lateStop = false;
  micDenied = true;
  mic.onclick(mic); await sleep(10);
  check('a refused microphone says so', mic.button.textContent === '⚠' && mic.button.title.indexOf('not allowed') >= 0);
  micDenied = false;
  var md = navigator.mediaDevices;
  navigator.mediaDevices = undefined;
  mic.onclick(mic);
  check('no microphone on an insecure page says how to get one', mic.button.title.indexOf('secure (https)') >= 0);
  navigator.mediaDevices = md;

  // On a desktop it starts off.
  coarse = false;
  delete store['gohort.readDictatedReplies'];
  speaker.onclick(speaker);
  check('on a desktop the first press turns it on', store['gohort.readDictatedReplies'] === 'on');
  var reads = readCalls.length;
  mic.onclick(mic); await sleep(10);
  await arrive({message: {role: 'assistant', spoken: true, content: 'Done.'}, bubble: card});
  check('it does not talk over someone recording', readCalls.length === reads);
  speaker.onclick(speaker);
  mic.onclick(mic); await sleep(10);
  pending[pending.length - 1].res(written('ok'));
  await sleep(10);
  await arrive({message: {role: 'assistant', spoken: true, content: 'Done.'}, bubble: card});
  check('nor with read-aloud off on this device', readCalls.length === reads);
  process.exit(fail ? 1 : 0);
})();
`

// dictationAudio is a browser with an audio context: tones it was asked to
// play, a level meter whose loudness the test sets, and a clock running twenty
// times fast so the silence and quiet waits pass in a second.
const dictationAudio = `
var made = 0, resumed = 0, tones = [], loud = 0, disconnected = 0;
window.AudioContext = function() { made++; this.state = 'suspended'; this.currentTime = 0; this.destination = {}; };
window.AudioContext.prototype.resume = function() { resumed++; this.state = 'running'; };
window.AudioContext.prototype.createOscillator = function() {
  var o = {frequency: {setValueAtTime: function(f) { o.from = f; }, linearRampToValueAtTime: function(f) { o.to = f; }},
    connect: function() {}, start: function() { tones.push(o.to > o.from ? 'up' : 'down'); }, stop: function() {}};
  return o;
};
window.AudioContext.prototype.createGain = function() {
  return {gain: {setValueAtTime: function() {}, exponentialRampToValueAtTime: function() {}}, connect: function() {}};
};
window.AudioContext.prototype.createMediaStreamSource = function() {
  return {connect: function() {}, disconnect: function() { disconnected++; }};
};
// loud = 0 is a meter with no signal at all, as a web view can give it; any
// real microphone hisses a little, which the float reading keeps and the
// byte one rounds away.
window.AudioContext.prototype.createAnalyser = function() {
  return {fftSize: 0, getByteTimeDomainData: function(b) {
    for (var i = 0; i < b.length; i++) b[i] = 128 + Math.round((i % 2 ? 1 : -1) * loud * 127);
  }, getFloatTimeDomainData: function(b) {
    for (var i = 0; i < b.length; i++) b[i] = (i % 2 ? 1 : -1) * loud;
  }};
};
var realNow = Date.now, t0 = realNow();
Date.now = function() { return t0 + (realNow() - t0) * 20; };
eval(dictationJS);
(async function() {
  await sleep(10);
  var mic = actions[0];
  mic.onclick(mic);
  check('the tap wakes the audio, where a phone allows it', made === 1 && resumed === 1);
  await sleep(30);
  check('a rising tone when the microphone opens', tones.join() === 'up');
  check('recording waits for the tone, so it is not heard', recorders.length === 0);
  await sleep(250);
  check('then records', recorders.length === 1 && recorders[0].started);
  check('and says it stops on silence', mic.button.title.indexOf('stops when you go quiet') >= 0);

  // A quiet room, then speech, then quiet: it stops and sends.
  loud = 0.003; await sleep(150);
  check('room noise is not speech', pending.length === 0);
  loud = 0.2; await sleep(400);
  check('the button rings with the level', parseFloat(mic.button.style.props['--ui-act-level']) > 0.9);
  check('talking keeps it recording', pending.length === 0);
  loud = 0.003; await sleep(400);
  check('going quiet after speech stops it and sends it', pending.length === 1);
  check('saying what the meter heard, for the log', /^live=1 voiced=\d+ first=\d+ peak=0\.\d{3}$/.test(pending[0].opts.headers['X-Dictation-Meter']));
  check('with a falling tone', tones.join() === 'up,down');
  check('the meter is let go and the ring cleared', disconnected === 1 && mic.button.style.props['--ui-act-level'] === '0.00');
  pending[0].res(written('Hello.'));
  await sleep(10);

  // A noisy room raises the floor: its noise is not speech, and speech over
  // it still is.
  mic.onclick(mic); await sleep(250);
  loud = 0.06; await sleep(400);
  check('a noisy room is not taken for speech', pending.length === 1);
  loud = 0.4; await sleep(500);
  loud = 0.06; await sleep(400);
  check('speech over it, then its noise again, stops it', pending.length === 2);
  pending[1].res(written('Hello again.'));
  await sleep(10);

  // Nothing said at all: after the quiet wait it cancels, and nothing is
  // sent to be written out.
  mic.onclick(mic); await sleep(250);
  loud = 0.003; await sleep(500);
  check('a short silence before speaking does not stop it', !recorders[2].stopped);
  await sleep(500);
  check('a long one with nothing said cancels, sending nothing', recorders[2].stopped && pending.length === 2 &&
    mic.button.textContent === '🎤' && mic.button.title.indexOf('Nothing was heard') === 0);
  check('with the falling tone, the meter let go and the ring cleared', tones[tones.length - 1] === 'down' &&
    !('aria-pressed' in mic.button.attrs) && mic.button.style.props['--ui-act-level'] === '0.00' && disconnected === 3);

  // Stop pressed before anything was said cancels the same way: a silent
  // clip sent anyway comes back as words the transcriber made up.
  mic.onclick(mic); await sleep(250);
  loud = 0.003; await sleep(100);
  mic.onclick(mic); await sleep(10);
  check('stop pressed with nothing heard cancels, sending nothing', recorders[3].stopped && pending.length === 2 &&
    mic.button.textContent === '🎤' && mic.button.title.indexOf('Nothing was heard') === 0 && tones[tones.length - 1] === 'down');
  mic.onclick(mic); await sleep(250);
  loud = 0.3; await sleep(500);
  mic.onclick(mic); await sleep(10);
  check('stop pressed after speaking sends it', recorders[4].stopped && pending.length === 3);
  pending[2].res(written('Yes.'));
  await sleep(10);
  // A word too short for the silence stop to count is still kept: a short
  // "yes" and then stop is sent, not thrown away.
  loud = 0.003;
  mic.onclick(mic); await sleep(250);
  loud = 0.3; await sleep(150);
  loud = 0.003;
  mic.onclick(mic); await sleep(10);
  check('a short word and then stop is sent', recorders[5].stopped && pending.length === 4);
  pending[3].res(written('Yes.'));
  await sleep(10);
  mic.onclick(mic); await sleep(250);
  loud = 0.3; await sleep(150);
  loud = 0.003; await sleep(900);
  check('so is a short word and then the long quiet', recorders[6].stopped && pending.length === 5);
  pending[4].res(written('Yes.'));
  await sleep(10);

  // A meter that never gets a signal knows nothing, so Stop sends.
  loud = 0;
  mic.onclick(mic); await sleep(250);
  await sleep(100);
  mic.onclick(mic); await sleep(10);
  check('a meter with no signal does not cancel a press', recorders[7].stopped && pending.length === 6);
  pending[5].res(written('Yes.'));
  await sleep(10);

  // Pressed during the tone: nothing records.
  var recs = recorders.length;
  mic.onclick(mic); await sleep(20); mic.onclick(mic); await sleep(250);
  check('pressing during the tone cancels before any recording', recorders.length === recs && mic.button.textContent === '🎤');
  process.exit(fail ? 1 : 0);
})();
`

// The mic as the chat runs it: records with a timer, writes the recording out
// into the box with a spinner, reads a dictated message's reply aloud once the
// turn is over (and only then, and only while switched on), stops a reply
// when typing starts, cancels, and says why it failed.
func TestDictationButton(t *testing.T) {
	runDictationHarness(t, dictationNode(t), dictationHarness)
}

// dictationNode is the node the harnesses run in, or skips the test.
func dictationNode(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "-p", "process.versions.node.split('.')[0]").Output(); err == nil {
		if major, _ := strconv.Atoi(strings.TrimSpace(string(out))); major < 18 {
			t.Skipf("node %s has no Blob or AbortController; CI runs 20", strings.TrimSpace(string(out)))
		}
	}
	return node
}

// With an audio context, the mic plays a tone as it opens and as it closes,
// and stops by itself when the speaker goes quiet, or after a long wait when
// nothing was said.
func TestDictationStopsOnSilence(t *testing.T) {
	node := dictationNode(t)
	runDictationHarness(t, node, dictationSetup+dictationAudio)
}

func runDictationHarness(t *testing.T, node, harness string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "dictation_test.js")
	src := "var dictationJS = " + strconv.Quote(dictationJS) + ";\n" + harness
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// The meter's note reaches the log as the page wrote it, and nothing else a
// page could put in the header does.
func TestTheMeterNoteKeepsToItsShape(t *testing.T) {
	if got := dictationMeterNote("live=1 voiced=150 first=-1 peak=0.310"); got != "live=1 voiced=150 first=-1 peak=0.310" {
		t.Errorf("a note as the page writes it: %q", got)
	}
	if got := dictationMeterNote("live=1\n[ERROR] forged <b>"); got != "live=1 forged b" {
		t.Errorf("a forged line: %q", got)
	}
	if got := dictationMeterNote(strings.Repeat("a", 200)); len(got) != 80 {
		t.Errorf("a long note is clipped to a line: %d", len(got))
	}
}
