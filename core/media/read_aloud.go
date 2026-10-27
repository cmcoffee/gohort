// Read aloud: the web chat's 🔊 on an agent's reply (ReadAloudScript).
//
// Streamed, so a long reply starts playing in about a second instead of after
// the whole clip is made: on a CPU-only speech server that is the difference
// between a second and half a minute. It takes two requests because a browser's
// audio player streams only what it fetches itself, by GET, and a reply is too
// long for a URL:
//
//  1. POST {"text": ...} makes the first sentence at once (so a failure is
//     reported with its reason, not as a player that never starts) and
//     answers {"url": ...}, a link only this user can open for a while.
//  2. GET that link plays the first sentence and then the rest, made a few
//     sentences at a time as the player goes, and made once however many
//     requests open the link. When the last listener closes it, the making
//     stops a moment later.
//
// The splitting is done here rather than asked of the speech server, so it
// works with any server that speaks mp3. core mounts ServeReadAloud at
// ReadAloudPath and below it, and hands it the signed-in user, since this
// package cannot read a session.

package media

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/snugforge/nfo"
)

// ReadAloudPath is where the chat page posts a reply, and below which the
// link it gets back lives.
const ReadAloudPath = "/api/speak"

const (
	// readAloudMaxChars bounds one read-aloud: about seven minutes of speech.
	readAloudMaxChars = 6000
	// readAloudFirstChars bounds the first piece, made before the link is
	// handed back: a first sentence longer than this is split, at a comma when
	// one is near, so nobody waits on it. About a second on a CPU-only server.
	readAloudFirstChars = 100
	// readAloudChunkChars bounds each later piece. Made a few sentences at a
	// time, so the voice keeps its phrasing across them; on a CPU-only server
	// a piece this size is made several times faster than it plays.
	readAloudChunkChars = 600
	// readAloudTTL is how long a link plays.
	readAloudTTL = 10 * time.Minute
	// readAloudMaxJobs bounds the links held at once; the oldest goes first.
	readAloudMaxJobs = 64
)

// readAloudJob is one reply waiting to be played: its first piece already
// made, the rest as text.
//
// The rest is made once per link, however many requests play it: a browser
// opens a media link twice at once (a probe, then the real fetch), and each
// making the reply on its own doubled the work and slowed both. The first
// request starts the maker; every request replays what is made and waits for
// the next piece.
type readAloudJob struct {
	user      string
	first     []byte
	firstType string
	rest      []string
	voice     string // the agent's voice, empty for the configured one
	expires   time.Time

	mu        sync.Mutex
	cond      *sync.Cond // a piece was made, or the maker stopped
	made      [][]byte   // the rest, as made so far, tags dropped
	making    bool       // the maker is running
	failed    bool       // the maker stopped on an error; nothing more will come
	listeners int
	cancel    context.CancelFunc
}

// ReadAloudVoice, when set, names the voice of the agent whose reply is read:
// orchestrate installs it, since this package knows nothing of agents. It is
// asked only about the user's own agents and answers "" for any other, which
// reads in the configured voice.
var ReadAloudVoice func(user, agentID string) string

// readAloudGrace is how long the maker keeps going with nobody listening, so
// a probe that closes just before the real request arrives costs nothing.
var readAloudGrace = 2 * time.Second

var (
	readAloudMu   sync.Mutex
	readAloudJobs = map[string]*readAloudJob{}
	readAloudNow  = time.Now // a test moves the clock
)

// ServeReadAloud answers a POST with a link to the reply spoken, and a GET of
// that link with the audio. user is the signed-in user, empty when nobody is.
func ServeReadAloud(w http.ResponseWriter, r *http.Request, user string) {
	if user == "" {
		http.Error(w, "sign in first", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if r.URL.Path == ReadAloudPath+"/preview" {
			servePreview(w, r)
			return
		}
		startReadAloud(w, r, user)
	case http.MethodGet:
		key := strings.TrimPrefix(r.URL.Path, ReadAloudPath+"/")
		if key == "voices" {
			serveVoices(w, r)
			return
		}
		streamReadAloud(w, r, user, key)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func startReadAloud(w http.ResponseWriter, r *http.Request, user string) {
	if !GetSpeechConfig().Enabled {
		http.Error(w, "speech is not set up: an admin turns it on under Capabilities, Speech (TTS)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text    string `json:"text"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	voice := ""
	if ReadAloudVoice != nil && strings.TrimSpace(req.AgentID) != "" {
		voice = ReadAloudVoice(user, strings.TrimSpace(req.AgentID))
	}
	text := readAloudText(req.Text)
	if text == "" {
		http.Error(w, "nothing in this reply can be read aloud", http.StatusBadRequest)
		return
	}
	pieces := readAloudPieces(text)
	first, ext, err := SpeakIn(r.Context(), pieces[0], voice)
	if err == nil && ext != ".mp3" && len(pieces) > 1 {
		// Only mp3 pieces join into one stream. A server that answers in
		// another format is asked for the whole reply in one piece instead.
		first, ext, err = SpeakIn(r.Context(), text, voice)
		pieces = pieces[:1]
	}
	if err != nil {
		if r.Context().Err() == nil {
			nfo.Log("[speech] read aloud for %s failed: %v", user, err)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	id := make([]byte, 16)
	rand.Read(id)
	key := hex.EncodeToString(id)
	readAloudMu.Lock()
	now := readAloudNow()
	for k, j := range readAloudJobs {
		if now.After(j.expires) {
			delete(readAloudJobs, k)
		}
	}
	for len(readAloudJobs) >= readAloudMaxJobs {
		oldest := ""
		for k, j := range readAloudJobs {
			if oldest == "" || j.expires.Before(readAloudJobs[oldest].expires) {
				oldest = k
			}
		}
		delete(readAloudJobs, oldest)
	}
	job := &readAloudJob{user: user, first: first, firstType: SpeechMIME(ext),
		rest: pieces[1:], voice: voice, expires: now.Add(readAloudTTL)}
	job.cond = sync.NewCond(&job.mu)
	readAloudJobs[key] = job
	readAloudMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"url": ReadAloudPath + "/" + key})
}

func streamReadAloud(w http.ResponseWriter, r *http.Request, user, key string) {
	readAloudMu.Lock()
	job, ok := readAloudJobs[key]
	if ok && readAloudNow().After(job.expires) {
		delete(readAloudJobs, key)
		ok = false
	}
	readAloudMu.Unlock()
	// Somebody else's link reads as no link: whether it exists is not theirs
	// to learn.
	if !ok || job.user != user {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", job.firstType)
	w.Header().Set("Cache-Control", "no-store")
	flush := http.NewResponseController(w)
	if _, err := w.Write(job.first); err != nil {
		return
	}
	flush.Flush()
	ctx := r.Context()
	job.listen()
	defer job.leave()
	// Wake this request's wait when its listener goes, so it does not hold on
	// for a piece nobody will hear.
	stopWake := context.AfterFunc(ctx, func() {
		job.mu.Lock()
		job.cond.Broadcast()
		job.mu.Unlock()
	})
	defer stopWake()
	for i := range job.rest {
		job.mu.Lock()
		for len(job.made) <= i && job.making && ctx.Err() == nil {
			job.cond.Wait()
		}
		var piece []byte
		if len(job.made) > i {
			piece = job.made[i]
		}
		job.mu.Unlock()
		if piece == nil || ctx.Err() != nil {
			return // the maker stopped, or the listener did
		}
		if _, err := w.Write(piece); err != nil {
			return
		}
		flush.Flush()
	}
}

// listen counts a request in, and starts the maker if nothing is making the
// rest: the first request, or one reopening a link whose maker was stopped
// when everyone left. It picks up from the last piece made.
func (j *readAloudJob) listen() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.listeners++
	if j.making || j.failed || len(j.made) >= len(j.rest) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.making, j.cancel = true, cancel
	go j.make(ctx, len(j.made))
}

// leave counts a request out. The last one out stops the maker, after the
// grace, if nobody has come back by then.
func (j *readAloudJob) leave() {
	j.mu.Lock()
	j.listeners--
	idle := j.listeners == 0
	j.mu.Unlock()
	if !idle {
		return
	}
	time.AfterFunc(readAloudGrace, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.listeners == 0 && j.making && j.cancel != nil {
			j.cancel()
		}
	})
}

// make speaks the rest from piece from on, one piece at a time, handing each
// to the listeners as it is ready.
func (j *readAloudJob) make(ctx context.Context, from int) {
	defer func() {
		j.mu.Lock()
		j.making = false
		j.cancel = nil
		j.cond.Broadcast()
		j.mu.Unlock()
	}()
	for i := from; i < len(j.rest); i++ {
		audio, ext, err := SpeakIn(ctx, j.rest[i], j.voice)
		if err == nil && ext != ".mp3" {
			err = fmt.Errorf("the server answered %s, which does not join an mp3 stream", ext)
		}
		if err != nil {
			if ctx.Err() == nil {
				nfo.Log("[speech] read aloud for %s stopped partway: %v", j.user, err)
				j.mu.Lock()
				j.failed = true
				j.mu.Unlock()
			}
			return
		}
		j.mu.Lock()
		j.made = append(j.made, withoutID3(audio))
		j.cond.Broadcast()
		j.mu.Unlock()
	}
}

// PreviewSentence is what a voice preview says: a reply's kind of sentence,
// so the voice is heard the way it will be used. Shared by the admin's
// Preview voice and an agent's.
const PreviewSentence = "Hi! This is how I'll sound when I answer one of your voice notes."

// servePreview speaks PreviewSentence in the voice a form holds, saved or
// not, on the deployment's speech server, for an agent's Preview voice. It
// answers as a form test does ({"ok", "message"|"error", "audio"}), so the
// form plays the clip. Any signed-in user may: it says one fixed sentence.
func servePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Voice string `json:"voice"`
	}
	result := map[string]any{"ok": false}
	reply := func() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !GetSpeechConfig().Enabled {
		result["error"] = "speech is not set up: an admin turns it on under Capabilities, Speech (TTS)"
		reply()
		return
	}
	voice := strings.TrimSpace(req.Voice)
	audio, ext, err := SpeakIn(r.Context(), PreviewSentence, voice)
	if err != nil {
		result["error"] = err.Error()
		reply()
		return
	}
	if voice == "" {
		voice = "the deployment's voice"
		if v := GetSpeechConfig().Voice; v != "" {
			voice += " (" + v + ")"
		}
	}
	result["ok"] = true
	result["message"] = "Speaking in " + voice + "."
	result["audio"] = "data:" + SpeechMIME(ext) + ";base64," + base64.StdEncoding.EncodeToString(audio)
	reply()
}

// serveVoices lists the speech server's voices for any signed-in user, for
// the agent form's Voice chips; the admin's own list needs an administrator.
// Empty while speech is off.
func serveVoices(w http.ResponseWriter, r *http.Request) {
	var voices []string
	if cfg := GetSpeechConfig(); cfg.Enabled {
		voices = SpeechVoices(r.Context(), cfg)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(VoiceChips(voices))
}

// readAloudText is the part of a reply worth reading aloud, cut at the end of
// the last sentence that fits readAloudMaxChars, or at a word when none does.
func readAloudText(reply string) string {
	text := textutil.SpeakableText(reply)
	if utf8.RuneCountInString(text) <= readAloudMaxChars {
		return text
	}
	cut := string([]rune(text)[:readAloudMaxChars])
	if i := strings.LastIndexAny(cut, ".!?"); i > len(cut)/2 {
		return cut[:i+1]
	}
	if i := strings.LastIndexAny(cut, " \n"); i > 0 {
		return strings.TrimSpace(cut[:i])
	}
	return cut
}

// readAloudPieces splits text into the pieces it is made in: the first
// sentence alone, so it plays soon, then whole sentences gathered up to
// readAloudChunkChars. A sentence too long for its piece is split at a comma
// or a space.
func readAloudPieces(text string) []string {
	var sentences []string
	start := 0
	runes := []rune(text)
	for i, r := range runes {
		end := false
		switch {
		case r == '\n':
			end = true
		case r == '.' || r == '!' || r == '?':
			end = i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n'
		}
		if end {
			if s := strings.TrimSpace(string(runes[start : i+1])); s != "" {
				sentences = append(sentences, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(string(runes[start:])); s != "" {
		sentences = append(sentences, s)
	}
	if len(sentences) == 0 {
		return nil
	}
	first := splitAtWords(sentences[0], readAloudFirstChars)
	pieces := []string{first[0]}
	rest := append(first[1:], sentences[1:]...)
	cur := ""
	for _, s := range rest {
		for _, part := range splitAtWords(s, readAloudChunkChars) {
			if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(part) > readAloudChunkChars {
				pieces = append(pieces, cur)
				cur = ""
			}
			if cur == "" {
				cur = part
			} else {
				cur += " " + part
			}
		}
	}
	if cur != "" {
		pieces = append(pieces, cur)
	}
	return pieces
}

// splitAtWords cuts s into parts of at most n characters, each ending at a
// comma when one is near the limit, at a space otherwise.
func splitAtWords(s string, n int) []string {
	var out []string
	for utf8.RuneCountInString(s) > n {
		head := string([]rune(s)[:n])
		cut := strings.LastIndex(head, ", ")
		if cut < len(head)/2 {
			cut = strings.LastIndex(head, " ")
		} else {
			cut++ // keep the comma with what it ends
		}
		if cut <= 0 {
			cut = len(head)
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// withoutID3 drops the ID3 tag a speech server puts at the head of each mp3 it
// makes, so a piece joined onto the stream adds sound and no stray header.
func withoutID3(b []byte) []byte {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return b
	}
	size := int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f)
	end := 10 + size
	if b[5]&0x10 != 0 {
		end += 10 // a footer follows the tag
	}
	if end > len(b) {
		return b
	}
	return b[end:]
}

// ReadAloudScript is the `<script>` that puts 🔊 on every agent reply in the
// web chat, for pages to embed in Page.ExtraHeadHTML the way they embed
// TranscribeRuntimeFlagScript. Empty while speech is off, so the button only
// exists where pressing it can work.
//
// Pressed, the button spins with elapsed seconds until the voice starts, then
// turns into a stop button while it plays. Pressing it again stops it, at
// either stage; pressing another reply's stops this one. A failure leaves a
// warning sign, with the reason on hover.
func ReadAloudScript() string {
	if !GetSpeechConfig().Enabled {
		return ""
	}
	return "<script>" + readAloudJS + "</script>"
}

// readAloudJS is ReadAloudScript's body, a variable of its own so a test can
// run it.
const readAloudJS = `(function(){
  var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏';
  var idle = '🔊', stopGlyph = '⏹', failGlyph = '⚠';
  var playing = null;
  function stop() {
    var p = playing;
    if (!p) return;
    playing = null;
    if (p.timer) clearInterval(p.timer);
    if (p.ctl) p.ctl.abort();
    if (p.audio) { p.audio.pause(); p.audio.removeAttribute('src'); p.audio.load(); }
    if (p.button) { p.button.textContent = idle; p.button.title = 'Read aloud'; }
  }
  function fail(btn, why) {
    if (!btn) return;
    btn.textContent = failGlyph;
    btn.title = 'Read aloud failed: ' + String(why || 'unknown error').trim() + ' Press to try again.';
  }
  function press(ctx) {
    var again = playing && playing.bubble === ctx.bubble;
    stop();
    if (again) return;
    var text = String(ctx.getText() || '');
    if (!text.trim()) return;
    var btn = ctx.button || null;
    var p = playing = {bubble: ctx.bubble, button: btn, ctl: new AbortController()};
    var started = Date.now(), frame = 0;
    if (btn) {
      btn.title = 'Getting the voice ready. Press to cancel';
      p.timer = setInterval(function() {
        var secs = Math.round((Date.now() - started) / 1000);
        btn.textContent = frames.charAt(frame++ % frames.length) + (secs >= 3 ? ' ' + secs + 's' : '');
      }, 100);
    }
    fetch('` + ReadAloudPath + `', {method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({text: text, agent_id: ctx.agentId || ''}), signal: p.ctl.signal})
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      })
      .then(function(d) {
        if (playing !== p) return;
        p.ctl = null;
        var a = p.audio = new Audio(d.url);
        a.addEventListener('playing', function() {
          if (playing !== p) return;
          if (p.timer) { clearInterval(p.timer); p.timer = null; }
          if (btn) { btn.textContent = stopGlyph; btn.title = 'Stop reading'; }
        });
        a.addEventListener('ended', function() { if (playing === p) stop(); });
        a.addEventListener('error', function() {
          if (playing !== p) return;
          stop();
          fail(btn, 'the audio stopped: the speech server may have failed partway.');
        });
        return a.play();
      })
      .catch(function(e) {
        if (e && e.name === 'AbortError') return;
        if (playing === p) stop();
        fail(btn, e && e.message || e);
      });
  }
  function register() {
    if (!window.uiRegisterBubbleAction) { setTimeout(register, 50); return; }
    window.uiRegisterBubbleAction({role: 'assistant', label: idle, title: 'Read aloud', onclick: press});
  }
  register();
})();`
