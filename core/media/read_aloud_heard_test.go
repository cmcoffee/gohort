package media

import (
	"bytes"
	"math"
	"net/http"
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
