package media

import (
	"fmt"
	"strings"
)

// readAloudHeard is the page's account of how one read ended, posted to the
// read's link once its audio stops: why, how much of it played, and how much
// of the reply the page had handed to be read. The stream's own line says
// what the server sent; this one says what was heard. Together they place a
// voice cut short: the player stopping before the end of what it was sent,
// the page not sending the rest, or the reply changing under the read.
type readAloudHeard struct {
	Why        string `json:"why"`
	Ended      bool   `json:"ended"`       // the audio played to its end
	PlayedMs   int    `json:"played_ms"`   // how far the audio got
	ReplyChars int    `json:"reply_chars"` // the reply's length when it stopped
	SentChars  int    `json:"sent_chars"`  // how much of it the page handed to be read
	ReplyDone  bool   `json:"reply_done"`  // the reply was finished
	Waiting    int    `json:"waiting"`     // reads opened after this one, not yet played
}

// line is the log line for a read the page says it heard, and whether it
// reads as cut short: stopped before its end, played well short of the audio
// made, or ended with nothing after it and the finished reply not all handed
// over. made is the seconds of audio made for the read, 0 when unknown.
func (h readAloudHeard) line(user, client string, made float64) (string, bool) {
	played := float64(h.PlayedMs) / 1000
	of := ""
	if made > 0 {
		of = fmt.Sprintf(" of %.1fs made", made)
	}
	reply := "still being written"
	if h.ReplyDone {
		reply = "finished"
	}
	line := fmt.Sprintf("[speech] read aloud for %s, as heard: %s; played %.1fs%s; %d of the reply's %d chars handed to be read, the reply %s",
		user, logSafe(h.Why), played, of, h.SentChars, h.ReplyChars, reply)
	if h.Waiting > 0 {
		line += fmt.Sprintf("; %d more read(s) waiting to play", h.Waiting)
	}
	line += " (" + client + ")"
	short := !h.Ended ||
		(made > 0 && played < made-1) ||
		(h.Waiting == 0 && h.ReplyDone && h.SentChars < h.ReplyChars)
	return line, short
}

// logSafe is page-sent text fit for one log line: no line breaks, and short.
func logSafe(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "..."
	}
	return s
}

// mp3Seconds is how long the mp3 frames in b play: 0 for anything that is
// not mp3. For the log, so a read the page says played 12s of can be set
// beside the 40s it was sent.
func mp3Seconds(b []byte) float64 {
	b = withoutID3(b)
	secs := 0.0
	for len(b) >= 4 {
		n, _, ok := mp3FrameInfo(b)
		if !ok || n > len(b) {
			break
		}
		samples := 576.0
		if (b[1]>>3)&3 == 3 {
			samples = 1152
		}
		secs += samples / float64(mp3SampleRate(b))
		b = b[n:]
	}
	return secs
}

// mp3SampleRate is the sample rate an mp3 frame header at the start of b
// says, for a header mp3FrameInfo has taken.
func mp3SampleRate(b []byte) int {
	version := (b[1] >> 3) & 3 // 3 = MPEG-1, 2 = MPEG-2, 0 = MPEG-2.5
	rate := [...]int{44100, 48000, 32000}[(b[2]>>2)&3]
	if version != 3 {
		rate /= 2
	}
	if version == 0 {
		rate /= 2
	}
	return rate
}
