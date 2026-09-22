package ui

// A pasted block is stood in for by "[Pasted text #N - X lines / Y chars]" in
// the composer, and expanded back before the send.
//
// The two halves drifted: the marker was WRITTEN with a middle dot and MATCHED
// with an em dash, so the pattern never fired. A marker with no stored entry
// is deliberately left as literal text, so the failure was silent — every
// paste delivered the placeholder to the model, the bubble and the transcript,
// and nothing anywhere said the content had been dropped.
//
// The comment above the producer already said the format must live in one
// place. It did; the reader of it was a second copy spelled out in a regex.

import (
	"regexp"
	"strings"
	"testing"
)

func panelJS(t *testing.T) string {
	t.Helper()
	return readRuntimeFile(t, "30_agent_loop_panel.js")
}

// The matcher depends on the STABLE parts only, so a change to the middle of
// the marker cannot break the round trip again.
func TestThePasteMatcherIgnoresTheMiddleOfTheMarker(t *testing.T) {
	src := panelJS(t)
	if !strings.Contains(src, `var pasteMarkerRE = /\[Pasted text #(\d+)[^\]]*\]/g;`) {
		t.Fatal("the paste matcher is not built from the stable parts")
	}
	// And nothing spells the middle out any more.
	if regexp.MustCompile(`Pasted text #\(\\d\+\) [^\[]*lines`).MatchString(src) {
		t.Error("a matcher still spells out the middle of the marker")
	}
}

// One producer, one matcher, and the matcher is the one the send uses.
func TestTheSendUsesTheSharedMatcher(t *testing.T) {
	src := panelJS(t)
	if !strings.Contains(src, "text.replace(pasteMarkerRE,") {
		t.Error("the send path does not use the shared matcher")
	}
	// Built in exactly one place. The other mention of the literal is the
	// cheap indexOf guard before the replace, which is not a second producer.
	if n := strings.Count(src, "'[Pasted text #' + n +"); n != 1 {
		t.Errorf("the marker is built in %d places; it must be built in one", n)
	}
}

// The producer and the matcher agree on a real marker. Checked by running the
// pattern the runtime ships against a marker of the shape it ships.
func TestAProducedMarkerMatchesTheShippedPattern(t *testing.T) {
	re := regexp.MustCompile(`\[Pasted text #(\d+)[^\]]*\]`)
	for _, marker := range []string{
		"[Pasted text #1 - 47 lines / 1834 chars]", // what it writes today
		"[Pasted text #1 — 47 lines / 1834 chars]", // and what a saved draft may still hold
	} {
		m := re.FindStringSubmatch("before " + marker + " after")
		if m == nil {
			t.Errorf("the shipped pattern does not match %q", marker)
			continue
		}
		if m[1] != "1" {
			t.Errorf("the pattern captured %q rather than the number", m[1])
		}
	}
	// A bracket that is not a marker is left alone.
	if re.MatchString("[not a marker]") {
		t.Error("the pattern matches ordinary bracketed text")
	}
}
