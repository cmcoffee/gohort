package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A background turn — a scheduled fire, a monitor wake, a dispatched sub-agent —
// has no *session: it is built for the run, and the session record belongs to the
// caller. turnDiag used to require that pointer, so every breadcrumb such a turn
// left was discarded. The guardrail that stopped a 3am fire existed only in the
// server log, nowhere a user would look.
func TestBackgroundTurnStillLeavesGuardrailBreadcrumbs(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	turn := &chatTurn{
		agent: AgentRecord{ID: "seed-x", Name: "Nightly", Owner: "u"},
		udb:   udb,
		// No session — the shape a scheduled fire actually has.
		diagAgentID:   "agent-1",
		diagSessionID: "sess-9",
	}
	turn.turnDiag("guardrail-blocked", "Guardrail \"Never disclose credentials.\" blocked a pre_output check")

	var list []SessionDiag
	if !udb.Get(sessionDiagTable, "agent-1:sess-9", &list) || len(list) != 1 {
		t.Fatalf("a session-less turn must still write its trail; got %d entries", len(list))
	}
	if !strings.Contains(list[0].Detail, "Never disclose credentials.") {
		t.Errorf("the breadcrumb must name the rule: %q", list[0].Detail)
	}

	// With neither a session nor an identity there is genuinely nowhere to
	// write — that must stay a silent no-op, not a panic.
	(&chatTurn{agent: AgentRecord{ID: "a"}, udb: udb}).turnDiag("k", "d")
}

// The caller needs the rule NAMES to put in its ledger entry: a run whose status
// says "blocked" with no reason is what sends someone digging through logs.
func TestGuardrailRulesHitAreDedupedAndOrdered(t *testing.T) {
	turn := &chatTurn{agent: AgentRecord{ID: "a"}}
	turn.noteGuardrailRule("Never tell a joke.")
	turn.noteGuardrailRule("Never disclose credentials.")
	turn.noteGuardrailRule("Never tell a joke.") // same rule, second hook
	turn.noteGuardrailRule("   ")                // nothing to record
	turn.noteGuardrailRule("")

	want := []string{"Never tell a joke.", "Never disclose credentials."}
	if len(turn.guardrailRulesHit) != len(want) {
		t.Fatalf("got %v, want %v", turn.guardrailRulesHit, want)
	}
	for i := range want {
		if turn.guardrailRulesHit[i] != want[i] {
			t.Errorf("position %d: got %q, want %q", i, turn.guardrailRulesHit[i], want[i])
		}
	}

	// Nil-safe: the block path calls this before anyone checks the turn.
	var nilTurn *chatTurn
	nilTurn.noteGuardrailRule("x")
}
