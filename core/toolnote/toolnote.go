// Package toolnote carries framework text appended to a tool's own result.
//
// Some results end with a note the framework wrote, not the tool: advice that
// the tool looks broken and should not be retried or worked around. That note
// is an instruction to the agent, deliberately, and everything that judges a
// result as outside content has to leave it out. The injection scan read one
// on a scheduled run, took "do not keep retrying it, and do not work around it
// with other tools" for instructions planted in what the tool fetched, and the
// run's card led with an injection warning for one refused API call.
//
// The note is set off by a mark rather than recognized by its wording, because
// wording is forgeable: a fetched page that ended with the right sentence
// would put whatever came after it past the scan. The mark is random per
// process, and it is removed before the model sees the result.
package toolnote

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

var mark = func() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "\x00gohort-note:" + hex.EncodeToString(b) + "\x00"
}()

// Append adds note to out as the framework's own text.
func Append(out, note string) string {
	if strings.TrimSpace(note) == "" {
		return out
	}
	return out + "\n\n" + mark + note
}

// Split separates the tool's own output from the framework note appended to
// it. note is "" when there is none.
func Split(s string) (body, note string) {
	i := strings.LastIndex(s, mark)
	if i < 0 {
		return s, ""
	}
	return strings.TrimRight(s[:i], "\n"), s[i+len(mark):]
}

// Strip removes the mark and keeps the note's text: the last step before a
// result reaches the model, for a path that never split it.
func Strip(s string) string { return strings.ReplaceAll(s, mark, "") }
