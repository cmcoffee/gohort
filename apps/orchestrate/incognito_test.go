package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Incognito is a deliberate user choice — "+ New ▾" opens a session stamped
// with it, and nothing in the tree sets it programmatically. The promise is
// "no baggage in, nothing out".
//
// The read half used to be enforced by runPlan blanking a local copy, which
// held for the orchestrator's prompt and nothing downstream: a turn builds
// several prompts, and runWorkerStep and runSynthesis each called facts()
// again and got the whole store back. So the clean room lasted exactly one
// prompt and the user was never told.
//
// Pinned at facts() rather than at a caller, because "which prompts a turn
// builds" is precisely the thing that changed underneath the old check.
func TestIncognitoWithholdsFactsFromEveryPrompt(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	agent, err := saveAgent(udb, AgentRecord{Name: "Agent", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	StoreMemoryFactP(udb, factsNamespace(agent.ID), "the user's dog is called Rex", FactWritePolicy{})

	// A connected session inherits, which is the default and must not change.
	normal := &chatTurn{user: "u", udb: udb, agent: agent, session: &ChatSession{ID: "s1"}}
	if got := normal.facts(); len(got) == 0 {
		t.Fatal("a connected session must still inherit stored facts")
	}

	// A clean room inherits nothing, at every prompt the turn builds.
	clean := &chatTurn{user: "u", udb: udb, agent: agent, session: &ChatSession{ID: "s2", Incognito: true}}
	if got := clean.facts(); len(got) != 0 {
		t.Fatalf("incognito session inherited %d fact(s); the clean room promises none", len(got))
	}
	// Same call the worker step, the synthesis step and the grounding judge each
	// make independently — the point of enforcing it here is that they agree
	// without any of them knowing about incognito.
	if got := UncheckedFactNotes(clean.facts()); len(got) != 0 {
		t.Fatalf("grounding judge scoped to %d note(s) an incognito prompt never showed", len(got))
	}

	// A turn with no session at all is not incognito — dispatch and scheduled
	// runs have no session and must keep their facts.
	sessionless := &chatTurn{user: "u", udb: udb, agent: agent}
	if got := sessionless.facts(); len(got) == 0 {
		t.Fatal("a sessionless turn (dispatch, schedule) must still inherit facts")
	}

	// DisableExplicit still wins independently of any of this.
	off := agent
	off.DisableExplicit = true
	if got := (&chatTurn{user: "u", udb: udb, agent: off}).facts(); len(got) != 0 {
		t.Fatal("DisableExplicit must still withhold facts")
	}
}
