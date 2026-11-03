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
// A reply still being written is read the same way, opened early: the POST
// says {"open": true}, and the page adds each sentence as it is written by
// POSTing {"text": ...} to the link, and {"done": true} when the reply is
// finished. The player waits for what is still to come. An open read nobody
// adds to for readAloudOpenIdle closes itself, so a closed page cannot hold a
// stream open.
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
	// one is near, so nobody waits on it. Under half a second on a GPU, and
	// it holds the whole of most short replies, which then play with no seam.
	readAloudFirstChars = 300
	// readAloudSecondChars bounds the second piece, made while the first
	// plays: a first piece can be one short sentence, a second or two of
	// speech, and this must be ready before it ends.
	readAloudSecondChars = 500
	// readAloudChunkChars bounds each later piece. Made a few sentences at a
	// time, so the voice keeps its phrasing across them and restarts less
	// often; on a GPU a piece this size is made about ten times faster than it
	// plays, so the maker stays well ahead.
	readAloudChunkChars = 1200
	// readAloudTTL is how long a link plays.
	readAloudTTL = 10 * time.Minute
	// readAloudMaxJobs bounds the links held at once; the oldest goes first.
	readAloudMaxJobs = 64
)

// readAloudOpenIdle is how long an open read waits for more text before it
// closes itself and the player plays out what it has. A var so a test can
// shorten it.
var readAloudOpenIdle = 30 * time.Second

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
	// open says the page may still add text: the reply is being written.
	// next is the piece of rest the maker takes next, so a piece from there
	// on may still grow by a sentence; chars counts what has been taken,
	// against readAloudMaxChars; idle closes an open read left alone.
	open  bool
	next  int
	chars int
	idle  *time.Timer

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
		switch {
		case r.URL.Path == ReadAloudPath+"/preview":
			servePreview(w, r)
		case strings.HasPrefix(r.URL.Path, ReadAloudPath+"/"):
			addToReadAloud(w, r, user, strings.TrimPrefix(r.URL.Path, ReadAloudPath+"/"))
		default:
			startReadAloud(w, r, user)
		}
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
		Open    bool   `json:"open"`
		// How the page came to read it, for the log line below: whether it
		// followed the reply as it was written, how long after the reply's
		// first text it started, and how many updates of the reply's text
		// the page had seen. A read that waited for the end with none seen
		// means the page never heard the reply being written.
		Live    bool `json:"live"`
		AfterMs int  `json:"after_ms"`
		Seen    int  `json:"seen"`
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
	if req.Live {
		nfo.Debug("[speech] read aloud for %s: following the reply as it is written, from %dms after its first text, %d chars in, %d updates seen (%s)",
			user, req.AfterMs, utf8.RuneCountInString(text), req.Seen, readAloudClient(r.UserAgent()))
	} else {
		nfo.Debug("[speech] read aloud for %s: a finished reply, %d chars, %d updates seen while it was written (%s)",
			user, utf8.RuneCountInString(text), req.Seen, readAloudClient(r.UserAgent()))
	}
	pieces := readAloudPieces(text)
	first, ext, err := SpeakIn(r.Context(), pieces[0], voice)
	open := req.Open
	if err == nil && ext != ".mp3" && (len(pieces) > 1 || open) {
		// Only mp3 pieces join into one stream. A server that answers in
		// another format is asked for the whole reply in one piece instead,
		// and an open read takes nothing more: what it has is what plays.
		if len(pieces) > 1 {
			first, ext, err = SpeakIn(r.Context(), text, voice)
		}
		pieces, open = pieces[:1], false
	}
	if err == nil && (len(pieces) > 1 || open) {
		// Its length frame gives the first piece's length, which WebKit
		// takes for the whole stream's and stops at.
		first = withoutLengthFrame(first)
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
		rest: pieces[1:], voice: voice, expires: now.Add(readAloudTTL),
		open: open, chars: utf8.RuneCountInString(text)}
	job.cond = sync.NewCond(&job.mu)
	if open {
		job.idle = time.AfterFunc(readAloudOpenIdle, job.close)
	}
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
	// How this request ended, said in the log: a voice cut short is the
	// player hanging up (it decided the audio was over), or the maker
	// stopping, and the two are fixed in different places. With the Range it
	// asked for and the kind of browser, since players differ in how they
	// take a stream of unknown length.
	sent, sentBytes, ended := 0, 0, "all pieces sent"
	defer func() {
		job.mu.Lock()
		total := 1 + len(job.rest)
		job.mu.Unlock()
		line := fmt.Sprintf("[speech] read aloud for %s: %d of %d piece(s), %d bytes, %s (range %q, %s)",
			user, sent, total, sentBytes, ended, r.Header.Get("Range"), readAloudClient(r.UserAgent()))
		if sent < total {
			nfo.Log("%s", line)
		} else {
			nfo.Debug("%s", line)
		}
	}()
	if _, err := w.Write(job.first); err != nil {
		ended = "the player hung up"
		return
	}
	sent, sentBytes = 1, len(job.first)
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
	// Until every piece is sent, including those an open read has still to
	// be given: the maker keeps going while the read is open, so a wait here
	// ends on a piece, on the read closing with nothing more, or on a stop.
	for i := 0; ; i++ {
		job.mu.Lock()
		for len(job.made) <= i && job.making && ctx.Err() == nil {
			job.cond.Wait()
		}
		var piece []byte
		if len(job.made) > i {
			piece = job.made[i]
		}
		finished := piece == nil && i >= len(job.rest) && !job.open && !job.failed
		job.mu.Unlock()
		if ctx.Err() != nil {
			ended = "the player hung up"
			return
		}
		if finished {
			return
		}
		if piece == nil {
			ended = "the speech maker stopped"
			return
		}
		if _, err := w.Write(piece); err != nil {
			ended = "the player hung up"
			return
		}
		sent, sentBytes = sent+1, sentBytes+len(piece)
		flush.Flush()
	}
}

// readAloudClient names the kind of browser a user agent is, for the log:
// WebKit (Safari, the desktop app's webview, every browser on an iPhone)
// takes a stream of unknown length differently from Chrome and Firefox.
func readAloudClient(ua string) string {
	switch {
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		return "Chrome"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "WebKit (iOS)"
	case strings.Contains(ua, "AppleWebKit/") && strings.Contains(ua, "Safari/"):
		return "Safari"
	case strings.Contains(ua, "AppleWebKit/"):
		return "WebKit (app)"
	case ua == "":
		return "no user agent"
	}
	return "other"
}

// listen counts a request in, and starts the maker if nothing is making the
// rest: the first request, or one reopening a link whose maker was stopped
// when everyone left. It picks up from the last piece made.
func (j *readAloudJob) listen() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.listeners++
	if j.making || j.failed || (len(j.made) >= len(j.rest) && !j.open) {
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
	// A stop wakes the wait below, which is for text an open read has not
	// been given yet.
	stopWake := context.AfterFunc(ctx, func() {
		j.mu.Lock()
		j.cond.Broadcast()
		j.mu.Unlock()
	})
	defer stopWake()
	for i := from; ; i++ {
		j.mu.Lock()
		for i >= len(j.rest) && j.open && ctx.Err() == nil {
			j.cond.Wait()
		}
		if i >= len(j.rest) || ctx.Err() != nil {
			j.mu.Unlock()
			return
		}
		text := j.rest[i]
		j.next = i + 1
		j.mu.Unlock()
		audio, ext, err := SpeakIn(ctx, text, j.voice)
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
		j.made = append(j.made, withoutLengthFrame(withoutID3(audio)))
		j.cond.Broadcast()
		j.mu.Unlock()
	}
}

// addToReadAloud adds what the page sends to an open read: {"text": ...} for
// the sentences written since, {"done": true} once the reply is finished.
// Answers {"open": false} when the read takes no more (closed, idle, full),
// so the page stops sending.
func addToReadAloud(w http.ResponseWriter, r *http.Request, user, key string) {
	var req struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	readAloudMu.Lock()
	job, ok := readAloudJobs[key]
	readAloudMu.Unlock()
	if !ok || job.user != user {
		http.NotFound(w, r)
		return
	}
	open := job.add(textutil.SpeakableText(req.Text), req.Done)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]bool{"open": open})
}

// add puts text on the end of an open read, a sentence at a time, and closes
// it when done or full. Joined to the last piece while the maker has not taken
// it and it has room, so the voice keeps its phrasing across sentences that
// arrive one by one. Reports whether the read takes more.
func (j *readAloudJob) add(text string, done bool) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.open {
		return false
	}
	if text = strings.TrimSpace(text); text != "" {
		if room := readAloudMaxChars - j.chars; utf8.RuneCountInString(text) > room {
			text, done = lastWholeSentence(string([]rune(text)[:max(room, 0)])), true
		}
		j.chars += utf8.RuneCountInString(text)
		for _, piece := range readAloudPiecesFrom(text, len(j.rest)+1) {
			last := len(j.rest) - 1
			if last >= j.next && utf8.RuneCountInString(j.rest[last])+1+utf8.RuneCountInString(piece) <= readAloudLimit(last+1) {
				j.rest[last] += " " + piece
				continue
			}
			j.rest = append(j.rest, piece)
		}
	}
	if done {
		j.open = false
		if j.idle != nil {
			j.idle.Stop()
		}
	} else if j.idle != nil {
		j.idle.Reset(readAloudOpenIdle)
	}
	j.cond.Broadcast()
	return j.open
}

// lastWholeSentence is text up to the end of its last finished sentence, or
// "" when none finished: a read cut for length ends on a whole sentence, never
// mid-word.
func lastWholeSentence(text string) string {
	if i := strings.LastIndexAny(text, ".!?"); i >= 0 {
		return strings.TrimSpace(text[:i+1])
	}
	return ""
}

// close ends an open read: the player plays out what it has.
func (j *readAloudJob) close() {
	j.mu.Lock()
	j.open = false
	j.cond.Broadcast()
	j.mu.Unlock()
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
	var voices []Voice
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

// readAloudPieces splits text into the pieces it is made in: whole sentences
// gathered up to each piece's limit (readAloudLimit), short at the start so
// it plays soon and long after. Only a sentence too long for a piece of its
// own is split, at a comma or a space.
func readAloudPieces(text string) []string {
	return readAloudPiecesFrom(text, 0)
}

// readAloudPiecesFrom is readAloudPieces for text that follows start pieces
// already made, so each piece gets the limit of its place in the reply.
func readAloudPiecesFrom(text string, start int) []string {
	var pieces []string
	cur := ""
	for _, s := range readAloudSentences(text) {
		// A sentence that does not fit starts the next piece whole, rather
		// than being cut to fill this one.
		if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(s) > readAloudLimit(start+len(pieces)) {
			pieces = append(pieces, cur)
			cur = ""
		}
		for _, part := range splitAtWords(s, readAloudLimit(start+len(pieces))) {
			if cur != "" && utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(part) > readAloudLimit(start+len(pieces)) {
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

// readAloudLimit is how many characters piece i may hold. The limits only
// grow, so a part cut to fit one piece fits every piece after it.
func readAloudLimit(i int) int {
	switch i {
	case 0:
		return readAloudFirstChars
	case 1:
		return readAloudSecondChars
	}
	return readAloudChunkChars
}

// readAloudSentences splits text at the end of each sentence and line. A dot
// inside a number or a name ("1.2", "x.com") ends nothing.
func readAloudSentences(text string) []string {
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
	return sentences
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

// withoutLengthFrame drops the frame an encoder (LAME, so ffmpeg) puts first in
// an mp3 to say how long that one file is: an Info or Xing frame, or a VBRI
// one. Joined into a stream, the first piece's said how long the whole stream
// was, and WebKit (Safari, the desktop app) stopped playing there with every
// piece downloaded: the reply was cut off after its first sentences. Chrome
// and Firefox play to the end of the data either way. The frame holds no
// sound. An ID3 tag before it is kept; anything not an mp3 frame is left
// alone.
func withoutLengthFrame(b []byte) []byte {
	off := len(b) - len(withoutID3(b))
	n, side, ok := mp3FrameInfo(b[off:])
	if !ok || off+n > len(b) {
		return b
	}
	frame := b[off : off+n]
	tag := func(at int) string {
		if at+4 > len(frame) {
			return ""
		}
		return string(frame[at : at+4])
	}
	if t := tag(4 + side); t != "Xing" && t != "Info" && tag(36) != "VBRI" {
		return b
	}
	out := make([]byte, 0, len(b)-n)
	return append(append(out, b[:off]...), b[off+n:]...)
}

// mp3FrameInfo reads the MPEG Layer III frame header at the start of b: the
// frame's length in bytes, and the length of the side information after the
// header, where an encoder's Info or Xing tag starts. ok is false for anything
// that is not such a header.
func mp3FrameInfo(b []byte) (length, side int, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return 0, 0, false
	}
	version, layer := (b[1]>>3)&3, (b[1]>>1)&3 // version 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5; layer 1 = III
	bitIdx, rateIdx, pad := int(b[2]>>4), int((b[2]>>2)&3), int((b[2]>>1)&1)
	if version == 1 || layer != 1 || bitIdx == 0 || bitIdx == 15 || rateIdx == 3 {
		return 0, 0, false
	}
	mono := b[3]>>6 == 3
	if version == 3 {
		kbps := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}[bitIdx]
		rate := [...]int{44100, 48000, 32000}[rateIdx]
		side = 32
		if mono {
			side = 17
		}
		return 144000*kbps/rate + pad, side, true
	}
	kbps := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}[bitIdx]
	rate := [...]int{22050, 24000, 16000}[rateIdx]
	if version == 0 {
		rate /= 2
	}
	side = 17
	if mono {
		side = 9
	}
	return 72000*kbps/rate + pad, side, true
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
  var speakPath = '` + ReadAloudPath + `';
  var playing = null;
  function stop() {
    var p = playing;
    if (!p) return;
    playing = null;
    if (p.timer) clearInterval(p.timer);
    if (p.stall) clearTimeout(p.stall);
    if (p.wake) clearTimeout(p.wake);
    if (p.settle) clearTimeout(p.settle);
    if (p.ctl) p.ctl.abort();
    (p.ctls || []).forEach(function(c) { c.abort(); });
    // An open read left behind would close itself in half a minute; saying
    // so now frees the speech server at once.
    if (p.live && p.segOpen && p.url) post(p.url, {done: true});
    if (p.bubble && p.bubble.__gohortFollow === p) p.bubble.__gohortFollow = null;
    if (p.audio) { p.audio.pause(); p.audio.removeAttribute('src'); p.audio.load(); }
    if (p.button) { p.button.textContent = idle; p.button.title = 'Read aloud'; }
    // Whoever asked hears how it ended: played to the end, or not (stopped,
    // replaced, failed). A caller reading several pieces in turn goes on
    // only after an end.
    if (p.onDone) { try { p.onDone(!!p.ended); } catch (e) {} }
  }
  function fail(btn, why) {
    if (!btn) return;
    btn.textContent = failGlyph;
    btn.title = 'Read aloud failed: ' + String(why || 'unknown error').trim() + ' Press to try again.';
  }
  // spin shows a read getting ready on its button: moving, and how long
  // once that is long enough to wonder about.
  function spin(p) {
    var btn = p.button, started = Date.now(), frame = 0;
    if (!btn) return;
    btn.title = 'Getting the voice ready. Press to cancel';
    p.timer = setInterval(function() {
      var secs = Math.round((Date.now() - started) / 1000);
      btn.textContent = frames.charAt(frame++ % frames.length) + (secs >= 3 ? ' ' + secs + 's' : '');
    }, 100);
  }
  function press(ctx) {
    var again = playing && playing.bubble === ctx.bubble;
    stop();
    if (again) return;
    // A reply still being written is followed as it is written.
    var live = ctx.bubble && ctx.bubble.__gohortText;
    if (live && !live.done) { follow(ctx); return; }
    var text = String(ctx.getText() || '');
    // Nothing to read counts as read, so a caller reading several replies
    // in turn goes on to the next.
    if (!text.trim()) { if (ctx.onDone) { try { ctx.onDone(true); } catch (e) {} } return; }
    var btn = ctx.button || null;
    var p = playing = {bubble: ctx.bubble, button: btn, ctl: new AbortController(), onDone: ctx.onDone};
    spin(p);
    fetch(speakPath, {method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({text: text, agent_id: ctx.agentId || '', seen: seenOf(ctx.bubble)}), signal: p.ctl.signal})
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      })
      .then(function(d) {
        if (playing !== p) return;
        p.ctl = null;
        // A player handed in is reused: the dictation script unlocks one on
        // the mic tap, since a phone plays sound only from a tap and the
        // reply arrives seconds later. Handlers are set, not added, so a
        // reused player carries this read's and no earlier one's.
        var a = p.audio = ctx.audio || new Audio();
        a.src = d.url;
        a.onplaying = function() {
          if (playing !== p) return;
          if (p.timer) { clearInterval(p.timer); p.timer = null; }
          if (btn) { btn.textContent = stopGlyph; btn.title = 'Stop reading'; }
        };
        a.onended = function() { if (playing === p) { p.ended = true; stop(); } };
        a.onerror = function() {
          if (playing !== p) return;
          stop();
          fail(btn, 'the audio stopped: the speech server may have failed partway.');
        };
        return a.play();
      })
      .catch(function(e) {
        if (e && e.name === 'AbortError') return;
        if (playing === p) stop();
        fail(btn, e && e.message || e);
      });
  }
  // --- Reading a reply while it is still being written ---
  //
  // A reply arrives far faster than it is spoken, so it is read from its
  // first sentence rather than from its last. The page says what a reply's
  // text is as it grows (ui-agent-message-text); follow hands an open read
  // each whole sentence as it is written, never half of one and nothing in
  // an unfinished code block, and closes the read when the reply is done.
  //
  // A reply that stops coming for stallMs has its read closed where it got
  // to, and what follows is read as the next, after it: a player left
  // waiting on a stream that has gone quiet may decide the audio is over and
  // drop the rest (WebKit does), and one that ends and starts again does not.
  var fence = String.fromCharCode(96, 96, 96);
  var startMinChars = 40;  // a first sentence shorter than this waits for more
  var startRate = 30;      // characters a second: twice speaking speed
  var startAhead = 150;    // about ten seconds of speech in hand starts it anyway
  var settleMs = 700;      // a reply quiet this long that ends a sentence has finished it
  var stallMs = 3000;      // a pause longer than this closes the read

  // speakableEnd is how far into text a read may go: the end of its last
  // whole sentence or line outside a code block, or all of it once the reply
  // is done. -1 when nothing is whole yet.
  //
  // A full stop counts once something follows it, since "3." may be "3.5".
  // The reply's last sentence has nothing after it, so it counted only once
  // the reply was done, which can be long after it was written (a check of
  // the finished reply held one back sixteen seconds). settled says the text
  // has been quiet for settleMs: then a sentence ending the text is whole.
  function speakableEnd(text, done, settled) {
    if (done) return text.length;
    var end = -1, inFence = false;
    for (var i = 0; i < text.length; i++) {
      if (text.substr(i, 3) === fence) { inFence = !inFence; i += 2; continue; }
      if (inFence) continue;
      var c = text.charAt(i), n = text.charAt(i + 1);
      if (c === '\n' || ((c === '.' || c === '!' || c === '?') && (n === ' ' || n === '\n' || (settled && i + 1 === text.length)))) end = i + 1;
    }
    return end;
  }
  function post(url, body, ctl) {
    return fetch(url, {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body), signal: ctl ? ctl.signal : undefined});
  }
  function follow(ctx) {
    var p = playing = {bubble: ctx.bubble, button: ctx.button || null, onDone: ctx.onDone, live: true,
      agentId: ctx.agentId || '', audio: ctx.audio || null, text: '', sent: 0, done: false,
      firstAt: 0, lastGrow: 0, started: false, segOpen: false, segStarting: false, url: '',
      urls: [], busy: false, ctls: [], chain: Promise.resolve()};
    spin(p);
    ctx.bubble.__gohortFollow = p;
    var now = ctx.bubble.__gohortText;
    feed(p, now.text, now.done, false);
  }
  // feed takes the reply's text as it is now. A replace that keeps what was
  // already sent carries on; one that took the reply back (empty), or
  // rewrote what was read, stops the read.
  function feed(p, text, done, replaced) {
    if (playing !== p) return;
    text = String(text || '');
    if (replaced || text.slice(0, p.sent) !== p.text.slice(0, p.sent)) {
      if (!text.trim()) { stop(); return; }
      var tail = p.text.slice(0, p.sent).trim().slice(-30);
      var at = tail ? text.indexOf(tail) : 0;
      if (at < 0) { stop(); return; }
      p.sent = tail ? at + tail.length : 0;
    }
    p.text = text;
    if (done) p.done = true;
    if (!p.firstAt && text.trim()) p.firstAt = Date.now();
    p.lastGrow = Date.now();
    armStall(p);
    if (p.settle) clearTimeout(p.settle);
    p.settle = p.done ? null : setTimeout(function() { p.settle = null; pump(p, true); }, settleMs);
    pump(p);
  }
  function armStall(p) {
    if (p.stall) clearTimeout(p.stall);
    p.stall = null;
    if (p.done || !p.segOpen) return;
    var left = Math.max(0, stallMs - (Date.now() - p.lastGrow));
    p.stall = setTimeout(function() {
      if (playing !== p || !p.segOpen) return;
      p.segOpen = false;
      send(p, {done: true});
    }, left);
  }
  // readyToStart: once a read has begun, whatever is whole goes on. A first
  // read waits for a sentence of some length, and for the reply to be coming
  // fast enough that the voice will not catch up with it, or for enough of
  // it in hand that a slow stretch can be talked through.
  //
  // The speed is how long that first sentence took to arrive, judged the
  // moment it is whole. It used to be watched for half a second first, and a
  // reply written in a second and a half (a fast model's usual) was nearly
  // over before the voice began. A slow model shows itself the same way: its
  // first sentence takes a second or more to come.
  function readyToStart(p, chunk) {
    if (p.started) return true;
    if (chunk.trim().length < startMinChars) return false;
    if (p.text.length - p.sent >= startAhead) return true;
    var waited = Math.max(1, Date.now() - p.firstAt);
    return p.text.length / (waited / 1000) >= startRate;
  }
  function pump(p, settled) {
    if (playing !== p || p.segStarting) return;
    var end = speakableEnd(p.text, p.done, settled);
    var chunk = end > p.sent ? p.text.slice(p.sent, end) : '';
    if (!p.segOpen) {
      if (!chunk.trim()) { p.sent = Math.max(p.sent, end); finishIfDone(p); return; }
      if (!p.done && !readyToStart(p, chunk)) {
        if (!p.wake) p.wake = setTimeout(function() { p.wake = null; pump(p); }, 250);
        return;
      }
      openRead(p, chunk, end);
      return;
    }
    if (!chunk.trim() && !p.done) return;
    p.sent = Math.max(p.sent, end);
    if (p.done) p.segOpen = false;
    send(p, {text: chunk, done: p.done});
  }
  function openRead(p, chunk, end) {
    p.segStarting = true;
    p.started = true;
    var ctl = new AbortController();
    p.ctls.push(ctl);
    post(speakPath, {text: chunk, agent_id: p.agentId, open: !p.done,
      live: true, after_ms: Date.now() - p.firstAt, seen: seenOf(p.bubble)}, ctl)
      .then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      })
      .then(function(d) {
        if (playing !== p) { post(d.url, {done: true}); return; }
        p.segStarting = false;
        p.sent = Math.max(p.sent, end);
        p.url = d.url;
        p.segOpen = !p.done;
        p.urls.push(d.url);
        armStall(p);
        playNext(p);
        pump(p);
      })
      .catch(function(e) {
        if (e && e.name === 'AbortError') return;
        if (playing === p) stop();
        fail(p.button, e && e.message || e);
      });
  }
  // send adds to the open read, in order. A read the server closed (left
  // idle, or full) takes no more: what follows starts the next.
  function send(p, body) {
    var url = p.url;
    p.chain = p.chain.then(function() {
      return post(url, body).then(function(r) { return r.ok ? r.json() : {open: false}; })
        .then(function(d) { if (playing === p && url === p.url && !d.open) p.segOpen = false; })
        .catch(function() {});
    });
  }
  function playNext(p) {
    if (playing !== p || p.busy || !p.urls.length) return;
    p.busy = true;
    var a = p.audio = p.audio || new Audio();
    a.src = p.urls.shift();
    a.onplaying = function() {
      if (playing !== p) return;
      if (p.timer) { clearInterval(p.timer); p.timer = null; }
      if (p.button) { p.button.textContent = stopGlyph; p.button.title = 'Stop reading'; }
    };
    a.onended = function() {
      if (playing !== p) return;
      p.busy = false;
      if (p.urls.length) playNext(p); else finishIfDone(p);
    };
    a.onerror = function() {
      if (playing !== p) return;
      stop();
      fail(p.button, 'the audio stopped: the speech server may have failed partway.');
    };
    var played = a.play();
    if (played && played.catch) played.catch(function(e) { if (playing === p) { stop(); fail(p.button, e && e.message || e); } });
  }
  // finishIfDone ends the read once the reply is done and all of it played.
  function finishIfDone(p) {
    if (playing !== p || !p.done || p.segOpen || p.segStarting || p.busy || p.urls.length) return;
    if (p.sent < p.text.length && p.text.slice(p.sent).trim()) return;
    p.ended = true;
    stop();
  }
  // seenOf is how many updates of a reply's text the page heard, for the
  // server's log of how a read began.
  function seenOf(bubble) { return (bubble && bubble.__gohortSeen) || 0; }
  window.addEventListener('ui-agent-message-text', function(e) {
    var d = e && e.detail;
    if (!d || !d.bubble) return;
    d.bubble.__gohortSeen = (d.bubble.__gohortSeen || 0) + 1;
    d.bubble.__gohortText = {text: d.text || '', done: !!d.done};
    var p = d.bubble.__gohortFollow;
    if (!p || playing !== p) return;
    if (d.struck) { stop(); return; }
    feed(p, d.text, d.done, d.replaced);
  });

  // For the dictation script: read a reply aloud on its own (the reply to a
  // dictated message), and stop whatever is playing (the person sent a
  // message, or started recording). ctx is a bubble action's: {bubble, getText, agentId, button?,
  // audio?, onDone?(ended)}. A reply still being written is followed.
  window.gohortReadAloud = press;
  window.gohortReadAloudStop = stop;
  function register() {
    if (!window.uiRegisterBubbleAction) { setTimeout(register, 50); return; }
    window.uiRegisterBubbleAction({role: 'assistant', label: idle, title: 'Read aloud', onclick: press});
  }
  register();
})();`
