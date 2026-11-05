package media

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// What the page heard is logged loud when the voice was cut short, and
// quietly when it played out with the whole reply handed over.
func TestWhatThePageHeardSaysWhereAVoiceWasCut(t *testing.T) {
	whole := readAloudHeard{Why: "played to its end", Ended: true, PlayedMs: 40000, ReplyChars: 900, SentChars: 900, ReplyDone: true}
	for _, c := range []struct {
		name  string
		h     readAloudHeard
		made  float64
		short bool
	}{
		{"played out, all of it handed over", whole, 40.2, false},
		{"played out with the reply still coming and nothing after", readAloudHeard{Why: "played to its end", Ended: true, PlayedMs: 9000, ReplyChars: 300, SentChars: 200}, 9, false},
		{"played out with the next read waiting", readAloudHeard{Why: "played to its end", Ended: true, PlayedMs: 9000, ReplyChars: 900, SentChars: 600, ReplyDone: true, Waiting: 1}, 9, false},
		{"stopped before its end", readAloudHeard{Why: "the reply was struck", PlayedMs: 3000, ReplyChars: 900, SentChars: 900, ReplyDone: true}, 40, true},
		{"the player ended well short of what it was sent", readAloudHeard{Why: "played to its end", Ended: true, PlayedMs: 12000, ReplyChars: 900, SentChars: 900, ReplyDone: true}, 40, true},
		{"the finished reply's tail never handed over", readAloudHeard{Why: "played to its end", Ended: true, PlayedMs: 30000, ReplyChars: 1262, SentChars: 900, ReplyDone: true}, 30, true},
		{"no audio length known", whole, 0, false},
	} {
		if _, short := c.h.line("alice", "Safari", c.made); short != c.short {
			t.Errorf("%s: short = %v, want %v", c.name, short, c.short)
		}
	}
	line, _ := readAloudHeard{Why: "the reply\nwas struck", PlayedMs: 3200, ReplyChars: 1262, SentChars: 900, ReplyDone: true, Waiting: 1}.line("alice", "WebKit (iOS)", 40)
	for _, want := range []string{"for alice", "the reply was struck;", "played 3.2s of 40.0s made", "900 of the reply's 1262 chars", "the reply finished", "1 more read(s) waiting", "(WebKit (iOS))"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line should say %q: %s", want, line)
		}
	}
}

// A note of how a read ended is its owner's to send and adds nothing to the
// read.
func TestAHeardNoteAddsNothingToTheRead(t *testing.T) {
	readAloudServer(t)
	url := openRead(t, "alice", "Here is the plan for tonight.")
	note := `{"heard":{"why":"played to its end","ended":true,"played_ms":1000,"reply_chars":29,"sent_chars":29},"text":"Not read."}`
	if w := readAloudDo("POST", url, "bob", note); w.Code != http.StatusNotFound {
		t.Errorf("another user's note was taken: %d", w.Code)
	}
	if w := readAloudDo("POST", url, "alice", note); w.Code != http.StatusNoContent {
		t.Errorf("the owner's note: %d %s", w.Code, w.Body.String())
	}
	addTo(t, url, "alice", `{"text":"Then we check the logs.","done":true}`)
	body := readAloudDo("GET", url, "alice", "").Body.String()
	if strings.Contains(body, "Not read") || !strings.Contains(body, "Then we check the logs.") {
		t.Errorf("the note should add nothing and leave the read open: %q", body)
	}
}

// An open read says how it closed, and text sent after it closed is refused.
func TestAnOpenReadSaysHowItClosed(t *testing.T) {
	newJob := func() *readAloudJob {
		j := &readAloudJob{open: true, user: "alice"}
		j.cond = sync.NewCond(&j.mu)
		return j
	}
	j := newJob()
	j.add("One.", false, "")
	if j.closed != "" {
		t.Errorf("a read still open has not closed: %q", j.closed)
	}
	j.add("", true, "")
	if j.closed != "the page said the reply was done" {
		t.Errorf("closed by done: %q", j.closed)
	}
	j = newJob()
	j.add("", true, "the reply\npaused")
	if j.closed != "the page closed it: the reply paused" {
		t.Errorf("closed by the page for a reason: %q", j.closed)
	}
	if j.add("Late.", false, "") || strings.Contains(strings.Join(j.rest, " "), "Late") {
		t.Errorf("a closed read took more: %q", j.rest)
	}
	prev := readAloudOpenIdle
	readAloudOpenIdle = 30 * time.Second
	t.Cleanup(func() { readAloudOpenIdle = prev })
	j = newJob()
	j.close()
	if j.closed != "nothing was added for 30s" {
		t.Errorf("closed by being left: %q", j.closed)
	}
	j = newJob()
	j.chars = readAloudMaxChars - 5
	j.add("Short. And a tail that does not fit.", false, "")
	if !strings.Contains(j.closed, "reached") {
		t.Errorf("closed by its length: %q", j.closed)
	}
}

// The seconds an mp3 plays, from its frames: MPEG-1 frames hold 1152
// samples, MPEG-2 576; a tag before them counts for nothing, and something
// not mp3 for nothing at all.
func TestMP3Seconds(t *testing.T) {
	one := mp3Frame(true, false, "", 0)
	two := mp3Frame(false, false, "", 0)
	for _, c := range []struct {
		name string
		in   []byte
		want float64
	}{
		{"three MPEG-1 frames at 44.1kHz", bytes.Repeat(one, 3), 3 * 1152.0 / 44100},
		{"two MPEG-2 frames at 24kHz", bytes.Repeat(two, 2), 2 * 576.0 / 24000},
		{"after an ID3 tag", append([]byte("ID3\x04\x00\x00\x00\x00\x00\x02xx"), one...), 1152.0 / 44100},
		{"a frame cut short", one[:100], 0},
		{"not an mp3", []byte("[Here is the plan.]"), 0},
	} {
		if got := mp3Seconds(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: %.4fs, want %.4fs", c.name, got, c.want)
		}
	}
}

// mp3After cuts at a frame: what plays in the first secs seconds goes, a
// leading tag with it, and seconds the piece was too short for are left for
// the next one.
func TestMP3After(t *testing.T) {
	f := mp3Frame(true, false, "", 0)
	dur := 1152.0 / 44100
	five := bytes.Repeat(f, 5)
	if got, left := mp3After(five, 2.5*dur); !bytes.Equal(got, bytes.Repeat(f, 2)) || left != 0 {
		t.Errorf("2.5 frames in: %d bytes left, %.4fs over", len(got), left)
	}
	tagged := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x02xx"), five...)
	if got, _ := mp3After(tagged, 0.5*dur); !bytes.Equal(got, bytes.Repeat(f, 4)) {
		t.Errorf("after a tag: %d bytes", len(got))
	}
	if got, left := mp3After(five, 7*dur); len(got) != 0 || math.Abs(left-2*dur) > 1e-9 {
		t.Errorf("past the end: %d bytes, %.4fs over, want %.4fs", len(got), left, 2*dur)
	}
	if got, _ := mp3After([]byte("[not mp3]"), 1); string(got) != "[not mp3]" {
		t.Errorf("not an mp3 is kept whole: %q", got)
	}
}

// A player that says it has ended can have stopped short. The read says how
// much audio is past where it stopped, and plays from there, cut at a frame,
// across the pieces it was made in.
func TestAReadPlaysOnFromWhereThePlayerStopped(t *testing.T) {
	frame := mp3Frame(true, false, "", 0)
	dur := 1152.0 / 44100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat(frame, 10)) // each piece, 10 frames
	}))
	t.Cleanup(srv.Close)
	prev := GetSpeechConfig()
	SetSpeechConfig(SpeechConfig{Provider: "peer:den", Endpoint: srv.URL, Enabled: true})
	t.Cleanup(func() { SetSpeechConfig(prev) })

	url := startLink(t, readAloudLong("The first sentence of a reply long enough for two pieces"))
	whole := readAloudDo("GET", url, "alice", "").Body.Bytes()
	frames := len(whole) / len(frame)
	if frames < 20 || len(whole)%len(frame) != 0 {
		t.Fatalf("want two or more pieces of whole frames: %d bytes", len(whole))
	}
	made := float64(frames) * dur

	at := int(15.5 * dur * 1000) // partway into the second piece
	w := readAloudDo("POST", url, "alice", `{"rest_after_ms":`+strconv.Itoa(at)+`}`)
	var rest struct {
		RestMs int  `json:"rest_ms"`
		More   bool `json:"more"`
	}
	json.Unmarshal(w.Body.Bytes(), &rest)
	if want := int((made - float64(at)/1000) * 1000); w.Code != http.StatusOK || rest.RestMs < want-1 || rest.RestMs > want+1 || rest.More {
		t.Errorf("rest after %dms: %d %+v, want %dms and nothing more coming", at, w.Code, rest, want)
	}
	if w := readAloudDo("POST", url, "bob", `{"rest_after_ms":0}`); w.Code != http.StatusNotFound {
		t.Errorf("another user asked about the read: %d", w.Code)
	}

	from := readAloudDo("GET", url+"?from_ms="+strconv.Itoa(at), "alice", "").Body.Bytes()
	if !bytes.Equal(from, whole[16*len(frame):]) {
		t.Errorf("from %dms: %d frames, want the %d after the 16th", at, len(from)/len(frame), frames-16)
	}
	if past := readAloudDo("GET", url+"?from_ms=999999", "alice", "").Body.Bytes(); len(past) != 0 {
		t.Errorf("from past the end: %d bytes", len(past))
	}
}
