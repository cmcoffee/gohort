package media

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// readAloudServer is a speech server that answers each piece with an ID3 tag
// and the piece's own text as its "audio", so a test can read back what was
// made and in what order. A piece reading "fail" is refused.
func readAloudServer(t *testing.T) (asked func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body["input"])
		mu.Unlock()
		if strings.Contains(body["input"], "fail") {
			http.Error(w, "voice not found", http.StatusBadRequest)
			return
		}
		w.Write(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x02xx"), "["+body["input"]+"]"...))
	}))
	t.Cleanup(srv.Close)
	prev := GetSpeechConfig()
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Enabled: true})
	t.Cleanup(func() { SetSpeechConfig(prev) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func readAloudDo(method, path, user, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	ServeReadAloud(w, httptest.NewRequest(method, path, strings.NewReader(body)), user)
	return w
}

// A reply read aloud: the POST makes the first sentence and hands back a link,
// and the link plays that sentence and then the rest, made as it goes, as one
// mp3 stream with no stray tags in the middle.
func TestAReplyIsReadAloudAsAStream(t *testing.T) {
	asked := readAloudServer(t)
	reply := "**Sure.** I checked the forecast for tomorrow. It will be sunny, with a high of 72. See https://example.com/x for more."
	w := readAloudDo("POST", ReadAloudPath, "alice", `{"text":`+strconv.Quote(reply)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var start struct{ URL string }
	json.Unmarshal(w.Body.Bytes(), &start)
	if !strings.HasPrefix(start.URL, ReadAloudPath+"/") {
		t.Fatalf("a link to play: %q", start.URL)
	}
	if got := asked(); len(got) != 1 || got[0] != "Sure." {
		t.Errorf("the POST makes only the first sentence: %q", got)
	}

	if w := readAloudDo("GET", start.URL, "bob", ""); w.Code != http.StatusNotFound {
		t.Errorf("another user's link should not play: %d", w.Code)
	}
	w = readAloudDo("GET", start.URL, "alice", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("play: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !w.Flushed {
		t.Error("each piece should be flushed to the player as it is made")
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "ID3") || strings.Count(body, "ID3") != 1 {
		t.Errorf("one tag, at the head of the stream: %q", body)
	}
	want := "[Sure.][I checked the forecast for tomorrow. It will be sunny, with a high of 72. See for more.]"
	if got := body[strings.Index(body, "["):]; got != want {
		t.Errorf("the stream plays the reply in order, without the markdown or the address:\n got %q\nwant %q", got, want)
	}
}

// flushCounter is a response that counts how often it was flushed.
type flushCounter struct {
	*httptest.ResponseRecorder
	flushes *int32
}

func (f flushCounter) Flush() { atomic.AddInt32(f.flushes, 1) }

// The first sentence reaches the player before the next is made: the point of
// streaming is that the listener is not waiting on the rest.
func TestTheFirstSentenceIsSentBeforeTheRestIsMade(t *testing.T) {
	var flushes int32
	seen := int32(-1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["input"] == "Then the rest." {
			atomic.StoreInt32(&seen, atomic.LoadInt32(&flushes))
		}
		w.Write(fakeMP3)
	}))
	defer srv.Close()
	prev := GetSpeechConfig()
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Enabled: true})
	t.Cleanup(func() { SetSpeechConfig(prev) })

	var start struct{ URL string }
	resp := readAloudDo("POST", ReadAloudPath, "alice", `{"text":"First. Then the rest."}`)
	json.Unmarshal(resp.Body.Bytes(), &start)
	if start.URL == "" {
		t.Fatalf("start: %d %s", resp.Code, resp.Body.String())
	}
	w := flushCounter{httptest.NewRecorder(), &flushes}
	ServeReadAloud(w, httptest.NewRequest("GET", start.URL, nil), "alice")
	if got := atomic.LoadInt32(&seen); got < 1 {
		t.Errorf("the first sentence should be flushed before the rest is made; flushes by then: %d", got)
	}
}

// Everything that cannot start says why, before any link is handed out.
func TestReadAloudRefusals(t *testing.T) {
	readAloudServer(t)
	cases := []struct {
		name, method, path, user, body string
		code                           int
	}{
		{"signed out", "POST", ReadAloudPath, "", `{"text":"hi"}`, http.StatusForbidden},
		{"wrong method", "PUT", ReadAloudPath, "alice", `{"text":"hi"}`, http.StatusMethodNotAllowed},
		{"broken body", "POST", ReadAloudPath, "alice", `{"text":`, http.StatusBadRequest},
		{"only code", "POST", ReadAloudPath, "alice", "{\"text\":\"```\\nls\\n```\"}", http.StatusBadRequest},
		{"server refuses the first sentence", "POST", ReadAloudPath, "alice", `{"text":"fail now. Then more."}`, http.StatusBadGateway},
		{"no such link", "GET", ReadAloudPath + "/nope", "alice", "", http.StatusNotFound},
		{"signed out link", "GET", ReadAloudPath + "/nope", "", "", http.StatusForbidden},
	}
	for _, c := range cases {
		if w := readAloudDo(c.method, c.path, c.user, c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.code)
		}
	}
	SetSpeechConfig(SpeechConfig{Enabled: false})
	if w := readAloudDo("POST", ReadAloudPath, "alice", `{"text":"hi"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("speech off: %d", w.Code)
	}
}

// The links held at once are bounded: the oldest goes first.
func TestReadAloudLinksAreBounded(t *testing.T) {
	readAloudServer(t)
	readAloudMu.Lock()
	readAloudJobs = map[string]*readAloudJob{}
	readAloudMu.Unlock()
	var first struct{ URL string }
	for i := 0; i < readAloudMaxJobs+5; i++ {
		w := readAloudDo("POST", ReadAloudPath, "alice", `{"text":"Hello."}`)
		if i == 0 {
			json.Unmarshal(w.Body.Bytes(), &first)
		}
	}
	readAloudMu.Lock()
	n := len(readAloudJobs)
	readAloudMu.Unlock()
	if n > readAloudMaxJobs {
		t.Errorf("%d links held, the bound is %d", n, readAloudMaxJobs)
	}
	if w := readAloudDo("GET", first.URL, "alice", ""); w.Code != http.StatusNotFound {
		t.Errorf("the oldest link should have gone first: %d", w.Code)
	}
}

// A link stops playing after a while.
func TestAReadAloudLinkExpires(t *testing.T) {
	readAloudServer(t)
	now := time.Now()
	readAloudNow = func() time.Time { return now }
	t.Cleanup(func() { readAloudNow = time.Now })
	var start struct{ URL string }
	json.Unmarshal(readAloudDo("POST", ReadAloudPath, "alice", `{"text":"Hello."}`).Body.Bytes(), &start)
	now = now.Add(readAloudTTL + time.Second)
	if w := readAloudDo("GET", start.URL, "alice", ""); w.Code != http.StatusNotFound {
		t.Errorf("an expired link should not play: %d", w.Code)
	}
}

// A server that answers in a format pieces cannot be joined in is asked for
// the whole reply at once.
func TestReadAloudWithoutMP3IsOnePiece(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, body["input"])
		w.Write(append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...))
	}))
	defer srv.Close()
	prev := GetSpeechConfig()
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Enabled: true})
	t.Cleanup(func() { SetSpeechConfig(prev) })

	var start struct{ URL string }
	json.Unmarshal(readAloudDo("POST", ReadAloudPath, "alice", `{"text":"One. Two. Three."}`).Body.Bytes(), &start)
	w := readAloudDo("GET", start.URL, "alice", "")
	if w.Header().Get("Content-Type") != "audio/wav" || len(asked) != 2 || asked[1] != "One. Two. Three." {
		t.Errorf("one whole clip: type=%q asked=%q", w.Header().Get("Content-Type"), asked)
	}
}

func TestReadAloudPieces(t *testing.T) {
	long := strings.Repeat("This sentence is here to fill the reply. ", 40)
	pieces := readAloudPieces(strings.TrimSpace("Hi there! " + long))
	if pieces[0] != "Hi there!" {
		t.Errorf("the first piece is the first sentence: %q", pieces[0])
	}
	for i, p := range pieces {
		if n := len([]rune(p)); n > readAloudChunkChars {
			t.Errorf("piece %d is %d characters", i, n)
		}
		if i > 0 && !strings.HasSuffix(p, ".") {
			t.Errorf("piece %d should end on a sentence: %q", i, p[len(p)-20:])
		}
	}
	if joined := strings.Join(pieces, " "); joined != strings.TrimSpace("Hi there! "+long) {
		t.Errorf("the pieces should hold the whole reply")
	}
	first := readAloudPieces(strings.Repeat("aaa bbb ccc ddd eee, ", 20) + "end.")
	if n := len([]rune(first[0])); n > readAloudFirstChars || !strings.HasSuffix(first[0], ",") {
		t.Errorf("a long first sentence is cut at a comma within %d: %q", readAloudFirstChars, first[0])
	}
	if got := readAloudPieces("Version 1.2 is out.\nTry it."); len(got) != 2 || got[0] != "Version 1.2 is out." {
		t.Errorf("a dot inside a number does not end a sentence, a line does: %q", got)
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
}

func TestWithoutID3(t *testing.T) {
	tagged := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x03abc"), 0xFF, 0xFB)
	if got := withoutID3(tagged); !bytes.Equal(got, []byte{0xFF, 0xFB}) {
		t.Errorf("tag dropped: %v", got)
	}
	footer := append([]byte("ID3\x04\x00\x10\x00\x00\x00\x01a3DI\x04\x00\x10\x00\x00\x00\x01"), 0xFF)
	if got := withoutID3(footer); !bytes.Equal(got, []byte{0xFF}) {
		t.Errorf("tag and footer dropped: %v", got)
	}
	bare := []byte{0xFF, 0xFB, 0x90}
	if got := withoutID3(bare); !bytes.Equal(got, bare) {
		t.Errorf("no tag, unchanged: %v", got)
	}
	short := []byte("ID3\x04\x00\x00\x00\x00\x7f\x7f")
	if got := withoutID3(short); !bytes.Equal(got, short) {
		t.Errorf("a tag claiming more than there is leaves the clip alone: %v", got)
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
// registry, a server that answers when told to, and an Audio element that
// records whether it plays and lets the test say when its sound starts.
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
global.Audio = function(src) { this.src = src; this.paused = true; this.on = {}; audios.push(this); };
Audio.prototype.addEventListener = function(t, f) { this.on[t] = f; };
Audio.prototype.play = function() { this.paused = false; return Promise.resolve(); };
Audio.prototype.pause = function() { this.paused = true; };
Audio.prototype.removeAttribute = function(a) { if (a === 'src') this.src = ''; };
Audio.prototype.load = function() {};
function link(url) { return {ok: true, json: function() { return Promise.resolve({url: url}); }}; }
function reply(text) { return {bubble: {}, button: {textContent: '🔊', title: 'Read aloud'}, getText: function() { return text; }}; }
var idle = '🔊';

eval(readAloudJS);
(async function() {
  check('puts a read-aloud button on replies', registered && registered.role === 'assistant' && registered.label === idle);
  var one = reply('Hello there.');
  registered.onclick(one);
  check('posts the reply', pending.length === 1 && pending[0].url === '/api/speak' && JSON.parse(pending[0].opts.body).text === 'Hello there.');
  await sleep(150);
  check('spins while the voice gets ready', /^[⠀-⣿]/.test(one.button.textContent));
  pending[0].res(link('/api/speak/abc'));
  await sleep(20);
  check('plays the link it was given', audios.length === 1 && audios[0].src === '/api/speak/abc' && !audios[0].paused);
  check('still spinning until the sound starts', /^[⠀-⣿]/.test(one.button.textContent));
  audios[0].on.playing();
  check('the button becomes stop once it plays', one.button.textContent === '⏹');
  registered.onclick(one);
  check('pressing again stops it and lets go of the stream', audios[0].paused && audios[0].src === '' && one.button.textContent === idle);

  registered.onclick(one);
  await sleep(10);
  registered.onclick(one);
  await sleep(10);
  check('pressing while it gets ready cancels it', one.button.textContent === idle && audios.length === 1);
  check('and drops the request to the server', pending[1].opts.signal.aborted === true);

  registered.onclick(one);
  pending[2].res(link('/api/speak/def'));
  await sleep(20);
  var two = reply('Second.');
  registered.onclick(two);
  check('another reply stops the first', audios[1].paused && one.button.textContent === idle);
  pending[3].res({ok: false, status: 502, text: function() { return Promise.resolve('speech: HTTP 400: voice not found'); }});
  await sleep(20);
  check('a failure to start says why', two.button.textContent === '⚠' && two.button.title.indexOf('voice not found') >= 0);

  var three = reply('Third.');
  registered.onclick(three);
  pending[4].res(link('/api/speak/ghi'));
  await sleep(20);
  audios[2].on.error();
  check('a stream that breaks says so', three.button.textContent === '⚠' && three.button.title.indexOf('partway') >= 0);
  registered.onclick(three);
  pending[5].res(link('/api/speak/jkl'));
  await sleep(20);
  audios[3].on.playing();
  audios[3].on.ended();
  check('the button comes back when it ends', three.button.textContent === idle);
  registered.onclick(reply('   '));
  check('an empty reply asks for nothing', pending.length === 6);
  process.exit(fail ? 1 : 0);
})();
`

// The read-aloud button as the chat runs it: spins until the voice starts,
// plays the link the server hands back, stops on a second press or another
// reply's, cancels a request still being made, and says why when it fails.
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
