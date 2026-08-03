package core

// A tool call written as a BRACKETED DIRECTIVE. The shape that reached a
// contact verbatim: "[CALL_FUNCTION] fetch_image: find a photo of …" wrapped in
// seven hundred characters of narration — invented progress, second thoughts,
// "OK found a good one" — none of which had happened.

import (
	"strings"
	"testing"
)

func TestABracketedCallDirectiveIsMarkupNotProse(t *testing.T) {
	// The prose scan is gated off for a long body that finished cleanly, so a
	// narrated call this size was never going to be extracted as prose. It has
	// to be recognized as markup, where length is irrelevant.
	long := "[CALL_FUNCTION] fetch_image: find a photo of Shazz Barbaric first. " +
		strings.Repeat("I'll grab his actual face from the thumbnail or wherever we found him before. ", 10)
	if !containsFakeToolCodeBlock(long) {
		t.Fatal("a bracketed call directive must be caught however long the reply is")
	}
	if got := extractFakeToolCodeName(long); got != "fetch_image" {
		t.Errorf("attempted tool name = %q, want fetch_image", got)
	}
	for _, variant := range []string{
		"[TOOL_CALL] image: make it spooky",
		"[FUNCTION_CALL]: web_search",
		"[call_function] find_image: a cat",
	} {
		if !containsFakeToolCodeBlock(variant) {
			t.Errorf("not caught: %q", variant)
		}
	}
}

func TestOrdinaryBracketsAreNotCallDirectives(t *testing.T) {
	for _, plain := range []string{
		"I'll [probably] need a minute.",
		"[ATTACH: photo.png]",
		"See section [4] of the report.",
		"[BACKGROUND TASK FINISHED] The work you started earlier is done.",
	} {
		if containsFakeToolCodeBlock(plain) {
			t.Errorf("false positive on ordinary text: %q", plain)
		}
	}
}

func TestTheDirectiveIsStrippedAndTheSentenceSurvives(t *testing.T) {
	in := "Working on it.\n[CALL_FUNCTION] fetch_image: a photo of the bridge\nI'll have that shortly."
	out := stripFakeToolCodeBlocks(in)
	if strings.Contains(out, "CALL_FUNCTION") {
		t.Errorf("the directive must not reach the user:\n%s", out)
	}
	for _, keep := range []string{"Working on it.", "I'll have that shortly."} {
		if !strings.Contains(out, keep) {
			t.Errorf("stripping took more than the directive — lost %q:\n%s", keep, out)
		}
	}
}
