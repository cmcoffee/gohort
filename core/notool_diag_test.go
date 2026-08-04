// The zero-tool-turn diagnostic.
//
// A turn that called nothing leaves no trail but its own words. Observed:
// "Wiwee, try again" answered in 66 characters with zero tool calls — and the
// framework recorded the length and nothing else, so whether that reply was an
// honest refusal or a fresh empty promise could not be established afterwards
// at all. Every other shape of turn is reconstructable from its tool calls.
package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTheZeroToolDiagnosticCarriesBothSides(t *testing.T) {
	line := noToolDiagLine(1, "Wiwee, try again", "Yeah, that one's still not working out.", false)

	// Both sides, because either alone is unreadable: the reply without the
	// request is a sentence with no question attached.
	if !strings.Contains(line, "try again") {
		t.Errorf("the request must be logged:\n%s", line)
	}
	if !strings.Contains(line, "still not working out") {
		t.Errorf("the reply must be logged — it is the whole point:\n%s", line)
	}
	// Greppable alongside COLLAPSE-DIAG, which is the neighbouring diagnostic.
	if !strings.Contains(line, "NOTOOL-DIAG round 1") {
		t.Errorf("the line must be greppable and name its round:\n%s", line)
	}
}

func TestAMaskedSessionLeaksNothing(t *testing.T) {
	// MaskDebugOutput exists because some sessions carry SSH credentials,
	// system facts and private files. A diagnostic is not worth leaking them,
	// and "we only log the reply, not the tool results" is not an argument —
	// the reply is where a model repeats what it just read.
	line := noToolDiagLine(2, "the root password is hunter2", "I've stored hunter2 for you.", true)

	for _, secret := range []string{"hunter2", "root password", "stored"} {
		if strings.Contains(line, secret) {
			t.Errorf("masked output leaked %q:\n%s", secret, line)
		}
	}
	// Still useful: lengths and the round survive, so the turn is still
	// visible as having happened.
	if !strings.Contains(line, "NOTOOL-DIAG round 2") || !strings.Contains(line, "masked") {
		t.Errorf("a masked line must still record the turn:\n%s", line)
	}
}

func TestTruncationNeverSplitsACharacter(t *testing.T) {
	// The replies that prompted all of this end in "🏚️👔". Slicing bytes at a
	// fixed offset writes invalid UTF-8 into the log, which was theoretical
	// while this only truncated tool names and stopped being the moment it
	// started carrying reply text.
	emoji := strings.Repeat("🏚️", 200) + "tail"
	for _, n := range []int{1, 2, 3, 7, 64, 199, 1000} {
		got := truncForLog(emoji, n)
		if !utf8.ValidString(got) {
			t.Errorf("truncForLog(…, %d) produced invalid UTF-8: %q", n, got)
		}
	}
	// Short input is returned untouched, ellipsis and all.
	if got := truncForLog("short", 1000); got != "short" {
		t.Errorf("untruncated input must pass through, got %q", got)
	}
	// Newlines are flattened so one turn is one log line.
	if got := truncForLog("a\nb", 10); got != "a b" {
		t.Errorf("newlines must flatten to keep the line greppable, got %q", got)
	}
}
