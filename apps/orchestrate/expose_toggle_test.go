package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func exposeTestDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

// THE BUG: the Hidden-implies-Exposed default ran on every save, so a Hidden
// agent could never be un-published. The save that set Exposed=false flipped
// it straight back, and the dashboard card the user was removing reappeared —
// "my attempts to disable it are not working."
func TestUnexposingAHiddenAgentSticks(t *testing.T) {
	db := exposeTestDB(t)
	base := AgentRecord{
		Name: "Helper", OrchestratorPrompt: "You help.",
		Owner: "craig@example.com", Hidden: true,
	}
	saved, err := saveAgent(db, base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !saved.Exposed {
		t.Fatal("hiding a NEW agent should still default it to exposed (it needs a surface)")
	}

	// The user now turns the dashboard card off. This must stick.
	saved.Exposed = false
	again, err := saveAgent(db, saved)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if again.Exposed {
		t.Fatal("Exposed was forced back on — the toggle is inoperable for Hidden agents")
	}

	// And it must survive further saves that touch unrelated fields.
	again.Description = "edited"
	third, err := saveAgent(db, again)
	if err != nil {
		t.Fatalf("third save: %v", err)
	}
	if third.Exposed {
		t.Error("a later unrelated save re-exposed the agent")
	}
}

// The default still has to fire when the user FIRST hides a visible agent,
// or hiding orphans it: out of the fleet and off the dashboard both.
func TestHidingAVisibleAgentDefaultsToExposed(t *testing.T) {
	db := exposeTestDB(t)
	visible, err := saveAgent(db, AgentRecord{
		Name: "Visible", OrchestratorPrompt: "p", Owner: "craig@example.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if visible.Exposed {
		t.Fatal("a non-hidden agent should not be auto-exposed")
	}
	visible.Hidden = true
	hidden, err := saveAgent(db, visible)
	if err != nil {
		t.Fatalf("hide: %v", err)
	}
	if !hidden.Exposed {
		t.Error("hiding a visible agent should default Exposed on, or it has no surface at all")
	}
}

// An ordinary visible agent must never be auto-published — that is what put
// unrelated chat agents on the dashboard as apps.
func TestVisibleAgentIsNeverAutoExposed(t *testing.T) {
	db := exposeTestDB(t)
	for i := 0; i < 3; i++ {
		rec, err := saveAgent(db, AgentRecord{
			ID: "stable-id", Name: "Chatty", OrchestratorPrompt: "p",
			Owner: "craig@example.com",
		})
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if rec.Exposed {
			t.Fatalf("save %d auto-exposed a visible agent", i)
		}
	}
}
