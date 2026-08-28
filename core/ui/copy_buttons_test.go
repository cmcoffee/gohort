package ui

import (
	"strings"
	"testing"
)

// A reply carries TWO copy buttons, and which one is wired to which function is
// the whole point of them.
//
// Copy takes the reply's text alone — you are pasting an answer somewhere, and
// a paste that arrives wrapped in "## Assistant (round 2)" and tool dumps has
// to be edited back down by hand. Copy turn takes the transcript, for when you
// need to show what actually happened.
//
// This is a guard because the wiring has already drifted once: Copy was
// repointed at copySubSession and copyAssistantMessage became dead code that
// nothing called, so every copy of an answer came out as a transcript.
func TestReplyHasBothCopyButtons(t *testing.T) {
	js := string(runtimeJS)

	for _, want := range []string{
		"}, ['Copy']);",      // the text-only one
		"}, ['Copy turn']);", // the transcript one
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the assistant action bar is missing a %s button", want)
		}
	}

	for _, wiring := range []struct{ handler, why string }{
		{"onclick: function(){ copyAssistantMessage(bubble, copyBtn); },",
			"Copy must take the reply's text alone"},
		{"onclick: function(){ copySubSession(bubble, copyTurnBtn); },",
			"Copy turn must take the whole exchange"},
	} {
		if !strings.Contains(js, wiring.handler) {
			t.Errorf("%s — expected wiring not found:\n  %s", wiring.why, wiring.handler)
		}
	}

	// Both go through the one clipboard helper, so the execCommand fallback
	// that makes copying work on a non-secure origin can't be fixed in one and
	// left broken in the other.
	if strings.Count(js, "function writeClipboard(") != 1 {
		t.Error("writeClipboard should be the single clipboard path for the copy buttons")
	}
	for _, fn := range []string{"copyAssistantMessage", "copySubSession"} {
		body := functionBody(js, "function "+fn+"(")
		if body == "" {
			t.Errorf("%s is gone from the runtime", fn)
			continue
		}
		if !strings.Contains(body, "writeClipboard(") {
			t.Errorf("%s writes to the clipboard by hand instead of through writeClipboard", fn)
		}
	}
}

// functionBody returns the source from a function's declaration to the start of
// the next top-level declaration in the same file — enough to assert on what a
// function calls without parsing JavaScript.
func functionBody(js, decl string) string {
	i := strings.Index(js, decl)
	if i < 0 {
		return ""
	}
	rest := js[i+len(decl):]
	if j := strings.Index(rest, "\n    function "); j >= 0 {
		return rest[:j]
	}
	return rest
}
