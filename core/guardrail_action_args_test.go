package core

// What the pre_action warden is allowed to read.
//
// It used to reuse formatArgs, whose 200-character cap was chosen for a confirm
// dialog and a loop-guard hash. A rule about what an agent SENDS someone was
// therefore applied to the first 200 characters of the message — everything
// after that was invisible to the check, and nothing said so.

import (
	"strings"
	"testing"
)

func TestGuardrailArgsReadFarMoreThanTheDisplayCap(t *testing.T) {
	body := strings.Repeat("x", 3000) + "SECRET"
	args := map[string]any{"text": body, "to": "+15550109999"}

	// The display formatter still stops at 200 — it feeds the confirm dialog
	// and the repeat-fail signature, and changing it would change what those
	// key on.
	if got := formatArgs(args); strings.Contains(got, "SECRET") {
		t.Error("the display formatter should still be short")
	}
	// The warden's does not.
	got := formatArgsForGuardrail(args)
	if !strings.Contains(got, "SECRET") {
		t.Errorf("the warden cannot see past %d chars — a content rule would judge a prefix", 200)
	}
	// Both arguments survive, and the key order is deterministic so the same
	// call never produces two different candidates.
	if !strings.Contains(got, "to: +15550109999") {
		t.Errorf("an argument was dropped:\n%s", got)
	}
	if strings.Index(got, "text:") > strings.Index(got, "to:") {
		t.Error("keys are not sorted; the same call would judge differently run to run")
	}
}

func TestGuardrailArgsStayBounded(t *testing.T) {
	// A tool argument can be a base64 image or a whole document. Unbounded
	// would make the check slow, expensive, and possibly too big to fit.
	huge := strings.Repeat("y", 200000)
	got := formatArgsForGuardrail(map[string]any{"blob": huge})
	if len([]rune(got)) > guardrailArgChars()+200 {
		t.Errorf("per-value cap not applied: %d runes", len([]rune(got)))
	}
	// And many large values must not multiply past the cap.
	many := map[string]any{}
	for i := 0; i < 40; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("k", i)] = strings.Repeat("z", 10000)
	}
	total := formatArgsForGuardrail(many)
	if limit := guardrailArgChars() * guardrailArgTotalFactor; len([]rune(total)) > limit+200 {
		t.Errorf("whole-candidate cap not applied: %d runes, limit %d", len([]rune(total)), limit)
	}
}

// TestTruncationIsAnnounced — a warden handed a silent prefix reports that it
// found nothing objectionable, which is true of the prefix and says nothing
// about the rest.
func TestTruncationIsAnnounced(t *testing.T) {
	got := formatArgsForGuardrail(map[string]any{"text": strings.Repeat("x", 200000)})
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "NOT shown") {
		t.Errorf("truncation must be visible to the judge:\n%s", got[len(got)-120:])
	}
	// A value that fits is passed through untouched — no marker, no noise.
	short := formatArgsForGuardrail(map[string]any{"text": "send it"})
	if strings.Contains(short, "truncated") {
		t.Errorf("a short value should not be marked: %q", short)
	}
}

// TestTruncationIsRuneSafe — cutting by bytes splits a multi-byte character and
// hands the judge a replacement glyph, which reads as corruption rather than as
// a trim. Same bug as the console title trim.
func TestTruncationIsRuneSafe(t *testing.T) {
	got := truncateRunes(strings.Repeat("é", 5000), 100)
	if !strings.HasPrefix(got, strings.Repeat("é", 100)) {
		t.Error("trim cut mid-character")
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("trim produced a replacement character")
		}
	}
	if n := len([]rune(strings.Repeat("é", 50))); truncateRunes(strings.Repeat("é", 50), 100) != strings.Repeat("é", n) {
		t.Error("a value under the cap was altered")
	}
}

// TestEmptyArgsStayEmpty — a consequential call with no arguments must not
// gain a stray marker or a blank line the warden then has to interpret.
func TestEmptyArgsStayEmpty(t *testing.T) {
	if got := formatArgsForGuardrail(nil); got != "" {
		t.Errorf("nil args produced %q", got)
	}
	if got := formatArgsForGuardrail(map[string]any{}); got != "" {
		t.Errorf("empty args produced %q", got)
	}
}
