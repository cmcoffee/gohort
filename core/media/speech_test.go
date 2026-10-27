package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A bare mp3 frame header: what a speech server sends for response_format mp3
// when it writes no ID3 tag.
var fakeMP3 = append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 64)...)

// Speak asks an OpenAI-compatible server for mp3 with the configured model,
// voice and key, and hands back the clip.
func TestSpeakSendsTheRequestAndReturnsTheClip(t *testing.T) {
	var got map[string]string
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(fakeMP3)
	}))
	defer srv.Close()

	cfg := SpeechConfig{Endpoint: srv.URL + "/v1/", Model: "kokoro", Voice: "af_heart", APIKey: "k1", Enabled: true}
	audio, ext, err := SpeakWith(context.Background(), cfg, "  Hello there.  ")
	if err != nil {
		t.Fatalf("speak: %v", err)
	}
	if ext != ".mp3" || len(audio) != len(fakeMP3) {
		t.Errorf("clip: ext=%q len=%d", ext, len(audio))
	}
	if path != "/v1/audio/speech" || auth != "Bearer k1" {
		t.Errorf("request: path=%q auth=%q", path, auth)
	}
	if got["input"] != "Hello there." || got["model"] != "kokoro" || got["voice"] != "af_heart" || got["response_format"] != "mp3" {
		t.Errorf("body: %v", got)
	}
}

// Blank model and voice are left out, so a server's own defaults apply rather
// than an empty name it would refuse.
func TestSpeakLeavesBlankModelAndVoiceToTheServer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write(fakeMP3)
	}))
	defer srv.Close()
	if _, _, err := SpeakWith(context.Background(), SpeechConfig{Endpoint: srv.URL, Enabled: true}, "hi"); err != nil {
		t.Fatalf("speak: %v", err)
	}
	if _, ok := got["model"]; ok {
		t.Errorf("model sent blank: %v", got)
	}
	if _, ok := got["voice"]; ok {
		t.Errorf("voice sent blank: %v", got)
	}
}

// Every way of getting no clip is an error that says which, never a "clip" of
// an error page.
func TestSpeakRefusesWhatIsNotAClip(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	on := SpeechConfig{Endpoint: srv.URL, Enabled: true}

	cases := []struct {
		name   string
		cfg    SpeechConfig
		text   string
		status int
		body   string
		want   string
	}{
		{"disabled", SpeechConfig{Endpoint: srv.URL}, "hi", 200, "", "disabled"},
		{"no endpoint", SpeechConfig{Enabled: true}, "hi", 200, "", "not configured"},
		{"no text", on, "   ", 200, "", "no text"},
		{"server error", on, "hi", 500, `{"error":"voice not found"}`, "HTTP 500: {\"error\":\"voice not found\"}"},
		{"a 200 with no audio", on, "hi", 200, `{"detail":"model loading"}`, "without audio"},
	}
	for _, c := range cases {
		status, body = c.status, c.body
		audio, _, err := SpeakWith(context.Background(), c.cfg, c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want it to mention %q", c.name, err, c.want)
		}
		if audio != nil {
			t.Errorf("%s: returned %d bytes of audio with an error", c.name, len(audio))
		}
	}
}

func TestSpeechExt(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		ext  string
		ok   bool
	}{
		{"id3 mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00rest"), ".mp3", true},
		{"bare mp3", fakeMP3, ".mp3", true},
		{"wav", append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...), ".wav", true},
		{"ogg", append([]byte("OggS\x00"), make([]byte, 32)...), ".ogg", true},
		{"json", []byte(`{"error":"x"}`), "", false},
		{"html", []byte("<html><body>502</body></html>"), "", false},
		{"empty", nil, "", false},
	}
	for _, c := range cases {
		if ext, ok := speechExt(c.in); ext != c.ext || ok != c.ok {
			t.Errorf("%s: speechExt = %q,%v want %q,%v", c.name, ext, ok, c.ext, c.ok)
		}
	}
}

// Read aloud: signed-in users only, only while speech is on, and only the part
// of a reply worth hearing; the clip comes back as something a browser plays.
func TestServeReadAloud(t *testing.T) {
	var spoken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		spoken = body["input"]
		if spoken == "fail" {
			http.Error(w, "voice not found", http.StatusBadRequest)
			return
		}
		w.Write(fakeMP3)
	}))
	defer srv.Close()
	prev := GetSpeechConfig()
	t.Cleanup(func() { SetSpeechConfig(prev) })

	do := func(method, user, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		ServeReadAloud(w, httptest.NewRequest(method, ReadAloudPath, strings.NewReader(body)), user)
		return w
	}
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Enabled: false})
	if w := do("POST", "alice", `{"text":"hi"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("speech off: %d", w.Code)
	}
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Enabled: true})
	cases := []struct {
		name, method, user, body string
		code                     int
	}{
		{"GET", "GET", "alice", "", http.StatusMethodNotAllowed},
		{"signed out", "POST", "", `{"text":"hi"}`, http.StatusForbidden},
		{"broken body", "POST", "alice", `{"text":`, http.StatusBadRequest},
		{"only code", "POST", "alice", "{\"text\":\"```\\nls\\n```\"}", http.StatusBadRequest},
		{"server refuses", "POST", "alice", `{"text":"fail"}`, http.StatusBadGateway},
	}
	for _, c := range cases {
		if w := do(c.method, c.user, c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.code)
		}
	}
	w := do("POST", "alice", `{"text":"**Sure.** See https://example.com/x for more."}`)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.Len() != len(fakeMP3) {
		t.Errorf("a reply read aloud: %d %q %d bytes", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
	if spoken != "Sure. See for more." {
		t.Errorf("read aloud without the markdown or the address: %q", spoken)
	}
}

// A long reply is read to the end of the last sentence that fits.
func TestReadAloudTextIsCutAtASentence(t *testing.T) {
	long := strings.Repeat("One sentence here. ", readAloudMaxChars/10)
	got := readAloudText(long)
	if n := len([]rune(got)); n > readAloudMaxChars || n < readAloudMaxChars/2 {
		t.Fatalf("cut to %d characters", n)
	}
	if !strings.HasSuffix(got, "here.") {
		t.Errorf("should end on a sentence: %q", got[len(got)-30:])
	}
	if got := readAloudText(strings.Repeat("word ", readAloudMaxChars)); strings.HasSuffix(got, "wor") || len([]rune(got)) > readAloudMaxChars {
		t.Errorf("with no sentence end, cut at a word: %q", got[len(got)-20:])
	}
	if got := readAloudText("Short."); got != "Short." {
		t.Errorf("a short reply is read whole: %q", got)
	}
}

// The button only exists where pressing it can work.
func TestReadAloudScriptOnlyWhileSpeechIsOn(t *testing.T) {
	prev := GetSpeechConfig()
	t.Cleanup(func() { SetSpeechConfig(prev) })
	SetSpeechConfig(SpeechConfig{Endpoint: "http://x", Enabled: false})
	if s := ReadAloudScript(); s != "" {
		t.Errorf("speech off should add nothing: %q", s)
	}
	SetSpeechConfig(SpeechConfig{Endpoint: "http://x", Enabled: true})
	if s := ReadAloudScript(); !strings.HasPrefix(s, "<script>") || !strings.Contains(s, "'"+ReadAloudPath+"'") {
		t.Errorf("speech on should add the script posting to %s: %.120s", ReadAloudPath, s)
	}
}

// readAloudHarness drives readAloudJS the way the chat does: the page's
// registry, a server that answers when told to, and an Audio that records
// whether it plays. Each check prints ok or FAIL.
const readAloudHarness = `
var fail = 0;
function check(label, cond) { if (cond) console.log('ok   ' + label); else { fail++; console.log('FAIL ' + label); } }
function sleep(ms) { return new Promise(function(r) { setTimeout(r, ms); }); }
var registered = null;
global.window = {uiRegisterBubbleAction: function(a) { registered = a; }};
var pending = [];
global.fetch = function(url, opts) {
  return new Promise(function(res, rej) {
    pending.push({url: url, opts: opts, res: res});
    opts.signal.addEventListener('abort', function() { var e = new Error('aborted'); e.name = 'AbortError'; rej(e); });
  });
};
var audios = [];
global.Audio = function(src) { this.src = src; this.paused = true; audios.push(this); };
Audio.prototype.play = function() { this.paused = false; return Promise.resolve(); };
Audio.prototype.pause = function() { this.paused = true; };
URL.createObjectURL = function() { return 'blob:clip'; };
URL.revokeObjectURL = function() {};
function clip() { return {ok: true, blob: function() { return Promise.resolve('mp3'); }}; }
function reply(text) { return {bubble: {}, button: {textContent: '\uD83D\uDD0A', title: 'Read aloud'}, getText: function() { return text; }}; }

eval(readAloudJS);
(async function() {
  check('puts a read-aloud button on replies', registered && registered.role === 'assistant' && registered.label === '\uD83D\uDD0A');
  var one = reply('Hello there.');
  registered.onclick(one);
  check('posts the reply', pending.length === 1 && pending[0].url === '/api/speak' && JSON.parse(pending[0].opts.body).text === 'Hello there.');
  await sleep(150);
  check('spins while the clip is made', /^[\u2800-\u28FF]/.test(one.button.textContent));
  pending[0].res(clip());
  await sleep(20);
  check('plays the clip', audios.length === 1 && !audios[0].paused);
  check('the button becomes stop', one.button.textContent === '\u23F9');
  registered.onclick(one);
  check('pressing again stops it', audios[0].paused && one.button.textContent === '\uD83D\uDD0A');

  registered.onclick(one);
  await sleep(10);
  registered.onclick(one);
  await sleep(10);
  check('pressing while it is made cancels it', one.button.textContent === '\uD83D\uDD0A' && audios.length === 1);
  check('and drops the request to the server', pending[1].opts.signal.aborted === true);

  registered.onclick(one);
  pending[2].res(clip());
  await sleep(20);
  var two = reply('Second.');
  registered.onclick(two);
  check('another reply stops the first', audios[1].paused && one.button.textContent === '\uD83D\uDD0A');
  pending[3].res({ok: false, status: 502, text: function() { return Promise.resolve('speech: HTTP 400: voice not found'); }});
  await sleep(20);
  check('a failure says why', two.button.textContent === '\u26A0' && two.button.title.indexOf('voice not found') >= 0);
  registered.onclick(reply('   '));
  check('an empty reply asks for nothing', pending.length === 4);
  process.exit(fail ? 1 : 0);
})();
`

// The read-aloud button as the chat runs it: spins while the clip is made,
// plays it, stops on a second press or another reply's, cancels a clip still
// being made, and says why when it fails.
func TestReadAloudButton(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "-p", "process.versions.node.split('.')[0]").Output(); err == nil {
		if major, _ := strconv.Atoi(strings.TrimSpace(string(out))); major < 16 {
			t.Skipf("node %s has no AbortController; CI runs 20", strings.TrimSpace(string(out)))
		}
	}
	script := filepath.Join(t.TempDir(), "read_aloud_test.js")
	src := "var readAloudJS = " + strconv.Quote(readAloudJS) + ";\n" + readAloudHarness
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
