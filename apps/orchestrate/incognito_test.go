package orchestrate

import (
	"strings"
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

// The write half. "Nothing out" is the other clause of the same promise, and it
// was unenforced: an incognito turn still wrote facts back, still wrote
// relationships into the durable graph, and could still DELETE a stored fact by
// an index its own prompt no longer showed it.
//
// Each refuses with a message the model can read. A silent no-op returning
// success is how an agent comes to report saving something it did not, and
// withholding the tools instead leaves the prompt describing capabilities that
// are not there — which gets improvised around rather than reported.
func TestIncognitoRefusesDurableMemoryWrites(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	agent, err := saveAgent(udb, AgentRecord{Name: "Agent", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	StoreMemoryFactP(udb, factsNamespace(agent.ID), "the user's dog is called Rex", FactWritePolicy{})
	before := len(ListMemoryFacts(udb, factsNamespace(agent.ID)))

	clean := &chatTurn{user: "u", udb: udb, agent: agent, session: &ChatSession{ID: "s2", Incognito: true}}

	// store_fact / remember(pin=true) both land here.
	out, err := clean.storeFactNote("something worth keeping", ClaimDomainUnknown)
	if err == nil {
		t.Fatalf("incognito stored a fact anyway: %q", out)
	}
	if !strings.Contains(err.Error(), "incognito") {
		t.Errorf("refusal must name the reason so the model can relay it; got: %v", err)
	}

	// link_entities writes into the same user's durable graph.
	_, err = clean.linkEntitiesToolDef().Handler(map[string]any{
		"subject": "Robin", "relation": "works at", "object": "Acme",
	})
	if err == nil {
		t.Error("incognito wrote a relationship into the durable graph")
	}

	// forget_fact is destructive AND blind here — the index refers to a facts
	// block this prompt does not carry.
	_, err = clean.forgetFactToolDef().Handler(map[string]any{"index": 1, "quote": "Rex"})
	if err == nil {
		t.Error("incognito deleted a stored fact by an index it could not see")
	}

	if after := len(ListMemoryFacts(udb, factsNamespace(agent.ID))); after != before {
		t.Fatalf("durable memory changed from %d to %d facts during a clean-room session", before, after)
	}

	// And the refusal is the exception, not the rule: a connected session is not
	// gated. Asserted on the predicate every one of the three guards reads,
	// rather than by driving a real write — the live path runs dedup, the
	// relevance gate and supersession judging, all of which want a worker LLM
	// this fixture has no reason to stand up.
	normal := &chatTurn{user: "u", udb: udb, agent: agent, session: &ChatSession{ID: "s1"}}
	if normal.incognitoSession() {
		t.Fatal("a connected session must not be treated as a clean room")
	}
}
