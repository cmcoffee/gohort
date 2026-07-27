package core

// The clean-finish gate on prose tool-call extraction, and the fallback
// that keeps a finished answer when the synthesized call turns out to be
// a phantom.
//
// From a live failure: an 8366-char answer came back with finish=stop and
// zero tool calls, the prose scan read a `moltbook` call out of the model
// REPORTING what it had already done, the loop-guard blocked it as a
// repeat, and the wedge spent 39s regenerating an answer already in hand.

import (
	"strings"
	"testing"
)

func gateTestTools() (map[string]ToolHandlerFunc, []Tool) {
	handlers := map[string]ToolHandlerFunc{
		"moltbook": func(args map[string]any) (string, error) { return "", nil },
	}
	defs := []Tool{{
		Name: "moltbook",
		Parameters: map[string]ToolParam{
			"action":  {Type: "string"},
			"content": {Type: "string"},
		},
		Required: []string{"action"},
	}}
	return handlers, defs
}

func TestParseTextToolCall_ProseGate(t *testing.T) {
	handlers, defs := gateTestTools()
	// The shape that actually fired: a REPORT of a completed action,
	// written in call notation. Indistinguishable from an announcement of
	// an intended one, which is why the scan can't tell them apart and the
	// caller has to.
	prose := `Cycle complete. I posted the reply via ` +
		`moltbook(action="reply_to_post", content="thanks for the thoughtful reply")` +
		` and it went through.`

	if got := ParseTextToolCall(prose, handlers, defs, true); got == nil {
		t.Fatal("with allowProse=true the prose scan should still fire — a model that only narrates its calls depends on it")
	}
	if got := ParseTextToolCall(prose, handlers, defs, false); got != nil {
		t.Errorf("with allowProse=false the prose scan must not fire, got %+v", got)
	}
}

func TestParseTextToolCall_MarkupIgnoresTheGate(t *testing.T) {
	handlers, defs := gateTestTools()
	// Machine markup is unambiguous, so it is extracted either way —
	// only the reading-of-English branch is gated.
	markup := `<function=moltbook>` +
		`<parameter=action>reply_to_post</parameter>` +
		`<parameter=content>hello</parameter>` +
		`</function>`
	for _, allowProse := range []bool{true, false} {
		got := ParseTextToolCall(markup, handlers, defs, allowProse)
		if got == nil || got.Name != "moltbook" {
			t.Errorf("allowProse=%v: markup must always parse, got %+v", allowProse, got)
		}
	}
}

func TestStripToolCallMarkup_LeavesProseAlone(t *testing.T) {
	// The fallback stores StripToolCallMarkup(content); if that ate plain
	// prose, returning it instead of regenerating would hand back nothing.
	answer := strings.Repeat("This is the model's real answer. ", 40)
	if got := StripToolCallMarkup(answer); strings.TrimSpace(got) != strings.TrimSpace(answer) {
		t.Errorf("prose with no markup must survive intact: got %d chars, want %d", len(got), len(answer))
	}
}
