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

func TestTheMarkActionIsRegistered(t *testing.T) {
	if !strings.Contains(blockedMarkScript, "uiRegisterClientAction('turn_blocked_report'") {
		t.Error("the mark's click is not wired to the action the server names")
	}
	// The GLYPH is the panel's now, not this script's: the server sets
	// ChatMessage.Mark, so it renders inline before the reply and survives a
	// reload with no app code involved.
	if strings.Contains(blockedMarkScript, "uiRegisterBlockRenderer") {
		t.Error("the mark is still drawn as a session-level block, which replay collapses")
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

// The bar reads [Private] [Clean] [⋯]: the mode toggles first, the overflow of
// per-visitor surfaces after them.
//
// It came out reversed because the order rule named .ui-agent-actions, which
// is not a direct child of the topbar — it sits inside an unclassed wrapper,
// and `order` only ranks siblings. The wrapper is what has to be ordered.
func TestTheOverflowSitsAfterTheModeToggles(t *testing.T) {
	if !strings.Contains(dashboardBarCSS, ".ui-agent-topbar > div:not(.ui-agent-extras-slot)") {
		t.Error("nothing orders the wrapper that actually holds the actions row")
	}
	// The extras slot (which carries the mode toggles) must rank before it.
	iExtras := strings.Index(dashboardBarCSS, ".ui-agent-extras-slot { order: 1")
	iWrap := strings.Index(dashboardBarCSS, ".ui-agent-topbar > div:not(.ui-agent-extras-slot) { order: 2")
	if iExtras < 0 || iWrap < 0 {
		t.Fatal("the two order rules are not both present")
	}
}
