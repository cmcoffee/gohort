package agents

// The renderer for the mark on a stopped turn.
//
// It rendered nothing at all on the first pass: addBlock in the agent-loop
// runtime does `if (!built || !built.wrap) return;`, so a renderer that hands
// back a bare DOM node is dropped without an error, without a warning, and
// without anything on screen. The contract is a {wrap} object, and it is worth
// a test because the failure mode is silence.

import (
	"strings"
	"testing"
)

func TestTheMarkRendererReturnsWhatAddBlockExpects(t *testing.T) {
	if !strings.Contains(blockedMarkScript, "return {wrap:") {
		t.Error("the renderer returns a bare node; addBlock drops anything without .wrap, silently")
	}
	if !strings.Contains(blockedMarkScript, "uiRegisterBlockRenderer('turn_blocked'") {
		t.Error("the renderer is not registered for the type the server emits")
	}
}

// The mark says that something was stopped and nothing about what stopped it.
// Everything the owner reads about the rule is added server-side from their own
// log; this script must not assert anything about it.
func TestTheMarkSaysNothingAboutTheRule(t *testing.T) {
	for _, leak := range []string{"guardrail", "Guardrail", "rule fired", "pre_output", "pre_action"} {
		if strings.Contains(blockedMarkScript, leak) {
			t.Errorf("the mark's own copy names the mechanism (%q); that is the owner's", leak)
		}
	}
	if !strings.Contains(blockedMarkScript, "blocked") {
		t.Error("the mark does not say anything was blocked")
	}
	// It sends the session and the sender's sentence, and nothing it could
	// assert about why.
	if !strings.Contains(blockedMarkScript, "session: session") {
		t.Error("the report does not carry the session the owner needs to find the entry")
	}
}
