package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func scopeTestTurn(t *testing.T) (*chatTurn, AgentRecord) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	other, err := saveAgent(db, AgentRecord{Name: "Moltbook Conversational Agent", Owner: owner, OrchestratorPrompt: "posts things"})
	if err != nil {
		t.Fatal(err)
	}
	return &chatTurn{
		udb:     db,
		user:    owner,
		agent:   AgentRecord{ID: "seed-builder", Name: "Builder", Owner: owner, Cortex: true},
		session: &ChatSession{ID: "s1", AgentID: "seed-builder"},
	}, other
}

// TestRecurringScopeReachesAnotherAgent: a recurring task belongs to the agent
// that made it, so an agent asked about someone else's schedule could not see
// it at all. Live, that produced neither an answer nor a refusal — the turn
// fetched a pile of unrelated records and narrated them.
func TestRecurringScopeReachesAnotherAgent(t *testing.T) {
	turn, other := scopeTestTurn(t)

	id, label, err := turn.recurringScope(map[string]any{})
	if err != nil || id != "seed-builder" {
		t.Fatalf("omitting agent must mean this agent: id=%q label=%q err=%v", id, label, err)
	}

	// "all" is the whole point of the escape hatch: the listing helper reads an
	// empty agent id as every task this USER owns, never another user's.
	id, _, err = turn.recurringScope(map[string]any{"agent": "all"})
	if err != nil || id != "" {
		t.Fatalf(`agent="all" must scope to every agent: id=%q err=%v`, id, err)
	}

	// By name, the way the user says it — the whole reason the model can act on
	// what it was told rather than hunting for an id.
	id, label, err = turn.recurringScope(map[string]any{"agent": "Moltbook Conversational Agent"})
	if err != nil || id != other.ID {
		t.Fatalf("by name: id=%q want %q (err=%v)", id, other.ID, err)
	}
	if !strings.Contains(label, "Moltbook") {
		t.Errorf("the label should name the agent so a cross-agent act says whose it is, got %q", label)
	}

	if id, _, err := turn.recurringScope(map[string]any{"agent": other.ID}); err != nil || id != other.ID {
		t.Errorf("by id: id=%q want %q (err=%v)", id, other.ID, err)
	}

	// An agent that does not exist is an error, not a silent fall back to this
	// agent's own tasks — which would answer a question about someone else's
	// schedule with your own.
	if _, _, err := turn.recurringScope(map[string]any{"agent": "No Such Agent"}); err == nil {
		t.Error("an unknown agent must be refused, not quietly scoped to self")
	}
}

// TestRecurringToolDeclaresTheAgentScope: the parameter is the whole fix, so
// its absence is the whole fix missing.
func TestRecurringToolDeclaresTheAgentScope(t *testing.T) {
	p, ok := (&chatTurn{}).recurringToolDef().Tool.Parameters["agent"]
	if !ok {
		t.Fatal("the recurring tool no longer offers `agent` — another agent's schedules are invisible again")
	}
	if p.Type != "string" {
		t.Errorf("agent is %q, want string", p.Type)
	}
	for _, want := range []string{"all", "list"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("agent's description does not mention %q: %s", want, p.Description)
		}
	}
}
