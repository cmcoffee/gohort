package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCloneOnlySeedHidden proves the seed-kb template stays out of the public
// surface: it can never become Exposed (saveAgent forces it off, even from a
// Hidden record that the reachability rule would otherwise auto-expose), and the
// public filter excludes it regardless of a stale Exposed flag.
func TestCloneOnlySeedHidden(t *testing.T) {
	if !isCloneOnlySeed("seed-kb") {
		t.Fatal("seed-kb should be a clone-only template seed")
	}
	for _, id := range []string{"seed-chat", "seed-research", "seed-builder", "custom-123"} {
		if isCloneOnlySeed(id) {
			t.Errorf("%s must NOT be treated as clone-only", id)
		}
	}

	db := &DBase{Store: kvlite.MemStore()}

	// A normal Hidden agent gets auto-exposed (reachability), so it stays
	// reachable — the behavior seed-kb must NOT get.
	normal, err := saveAgent(db, AgentRecord{
		ID: "custom-hidden", Owner: "alice", Name: "Hidden One",
		OrchestratorPrompt: "x", Hidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !normal.Exposed {
		t.Error("a normal Hidden agent should be auto-exposed for reachability")
	}

	// seed-kb, even saved Hidden AND Exposed=true, must come back NOT exposed.
	kb, err := saveAgent(db, AgentRecord{
		ID: "seed-kb", Owner: seedOwner, Name: "Knowledge Base",
		OrchestratorPrompt: "x", Hidden: true, Exposed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if kb.Exposed {
		t.Error("seed-kb must never be Exposed (template seed)")
	}

	// The read-side guard hides it even if a stale record still carries
	// Exposed=true (e.g. a shadow saved before this fix).
	if publiclyExposable(AgentRecord{ID: "seed-kb", Exposed: true}) {
		t.Error("publiclyExposable must exclude seed-kb regardless of the Exposed flag")
	}
	if !publiclyExposable(AgentRecord{ID: "custom-x", Exposed: true}) {
		t.Error("a normal Exposed agent should still be publicly exposable")
	}
}
