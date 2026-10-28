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
function fire(t) { (listeners[t] || []).forEach(function(f) { f({}); }); }
var mic = null, micDenied = false, tracksStopped = 0;
Object.defineProperty(global, 'navigator', {configurable: true, value: {mediaDevices: {getUserMedia: function() {
  if (micDenied) { var e = new Error('denied'); e.name = 'NotAllowedError'; return Promise.reject(e); }
  return Promise.resolve({getTracks: function() { return [{stop: function() { tracksStopped++; }}]; }});
}}}});
window.navigator = navigator;
var recorders = [];
global.MediaRecorder = function(s) { this.mimeType = 'audio/webm;codecs=opus'; recorders.push(this); };
MediaRecorder.prototype.start = function() { this.started = true; };
MediaRecorder.prototype.stop = function() {
  this.ondataavailable({data: new Blob(['sound'], {type: 'audio/webm'})});
  this.onstop();
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
  check('and it is no longer marked as recording', !('aria-pressed' in mic.button.attrs));
  await sleep(150);
  check('a spinner while it is written out', /^[⠀-⣿]/.test(mic.button.textContent));
  pending[0].res(written('Meet me at noon.'));
  await sleep(10);
  check('the words land in the box to be read over', input.value === 'Meet me at noon.');
  check('and the button is ready again', mic.button.textContent === '🎤');
  check('the send tells the agent its reply will be heard', extras.length === 1 && extras[0].spoken_reply === true);
  check('with read-aloud on it goes at once, hands-free', sends === 1);

  // The reply to the dictated message is read once the turn is over: its
  // last reply, not a note before it.
  fire('ui-agent-sent');
  var note = {role: 'assistant', rawText: 'Checking.', wrap: {querySelector: function() { return null; }}};
  var answer = {role: 'assistant', rawText: 'Noon works.', wrap: {querySelector: function() { return {title: 'Read aloud'}; }}};
  decorators.forEach(function(d) { d(note); d(answer); });
  check('nothing is read before the turn is over', readCalls.length === 0);
  fire('ui-agent-turn-done');
  check('the last reply of the turn is read aloud', readCalls.length === 1 && readCalls[0].getText() === 'Noon works.' && readCalls[0].bubble === answer.wrap);
  check('in the unlocked player, for the right agent, with its button', readCalls[0].audio && readCalls[0].agentId === 'a1' && readCalls[0].button);

  // A typed message's reply is not read.
  fire('ui-agent-sent');
  decorators.forEach(function(d) { d(answer); });
  fire('ui-agent-turn-done');
  check('a typed message is not answered aloud', readCalls.length === 1);

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
  fire('ui-agent-sent');
  decorators.forEach(function(d) { d(answer); });
  fire('ui-agent-turn-done');
  check('switched off, it is not read', readCalls.length === 1);

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
  check('a transcriber failure says why and keeps the recording', mic.button.textContent === '↻ 0:00' &&
    mic.button.title.indexOf('HTTP 500') >= 0 && mic.button.title.indexOf('send the 0:00 recording again') >= 0);
  check('with the ✕ shown to discard it', discard.button.style.display === '');
  recs = recorders.length;
  mic.onclick(mic); await sleep(10);
  check('pressing sends the same recording again, recording nothing new', pending.length === 8 && pending[7].opts.body === kept && recorders.length === recs);
  check('with the spinner', /^[⠀-⣿]/.test(mic.button.textContent));
  mic.onclick(mic);
  check('cancelling the resend keeps it', pending[7].opts.signal.aborted && mic.button.textContent === '↻ 0:00' && discard.button.style.display === '');
  mic.onclick(mic); await sleep(10);
  pending[8].rej(new TypeError('Failed to fetch'));
  await sleep(10);
  check('a dropped network keeps it too', mic.button.textContent === '↻ 0:00' && mic.button.title.indexOf('Failed to fetch') >= 0);
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
window.AudioContext.prototype.createAnalyser = function() {
  return {fftSize: 0, getByteTimeDomainData: function(b) {
    for (var i = 0; i < b.length; i++) b[i] = 128 + Math.round((i % 2 ? 1 : -1) * loud * 127);
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
  check('with a falling tone', tones.join() === 'up,down');
  check('the meter is let go and the ring cleared', disconnected === 1 && mic.button.style.props['--ui-act-level'] === '0.00');
  pending[0].res(written('Hello.'));
  await sleep(10);

  // A noisy room raises the floor: its noise is not speech, and speech over
  // it still is.
  mic.onclick(mic); await sleep(250);
  loud = 0.06; await sleep(400);
  check('a noisy room is not taken for speech', pending.length === 1);
  loud = 0.4; await sleep(300);
  loud = 0.06; await sleep(400);
  check('speech over it, then its noise again, stops it', pending.length === 2);
  pending[1].res(written('Hello again.'));
  await sleep(10);

  // Nothing said at all: it stops after the quiet wait.
  mic.onclick(mic); await sleep(250);
  loud = 0.003; await sleep(500);
  check('a short silence before speaking does not stop it', pending.length === 2);
  await sleep(500);
  check('a long one with nothing said does', pending.length === 3);
  pending[2].res(written(''));
  await sleep(10);

  // Pressed during the tone: nothing records.
  mic.onclick(mic); await sleep(20); mic.onclick(mic); await sleep(250);
  check('pressing during the tone cancels before any recording', recorders.length === 3 && mic.button.textContent === '🎤');
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
