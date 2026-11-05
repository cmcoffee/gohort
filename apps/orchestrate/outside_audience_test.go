package orchestrate

import (
	"errors"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A person outside gohort reaches only an agent its owner set to serve them;
// anyone else's run is unaffected by the setting.
func TestAnOutsidePersonReachesOnlyAnAgentSetToServeThem(t *testing.T) {
	person := agenthost.OutsideScope("key1", "cust-7")
	if err := outsideRunRefusal(person, AgentRecord{ID: "a1"}); !errors.Is(err, agenthost.ErrNotForOutsideAudience) {
		t.Errorf("an agent not set to serve people outside ran for one: %v", err)
	}
	if err := outsideRunRefusal(person, AgentRecord{ID: "a1", OutsideAudience: true}); err != nil {
		t.Errorf("an agent set to serve people outside refused one: %v", err)
	}
	for _, user := range []string{"alice", "phantom:991", "app:servitor:x"} {
		if err := outsideRunRefusal(user, AgentRecord{ID: "a1"}); err != nil {
			t.Errorf("%s's run was refused by the outside setting: %v", user, err)
		}
	}
}

// What reaches an outside person from underneath is fixed, whatever the
// agent's share switches say: its saved notes, read-only, so it knows its job;
// never the owner's cortex or what the agent worked out across the owner's
// conversations. Published documents still reach them.
func TestAnOutsidePersonGetsTheAgentsNotesAndNothingOfTheOwners(t *testing.T) {
	person := agenthost.OutsideScope("key1", "cust-7")
	for _, rec := range []AgentRecord{
		{ID: "a1", Cortex: true, OutsideAudience: true},                            // defaults
		{ID: "a1", Cortex: true, OutsideAudience: true, ShareMemoryExplicit: true}, // every switch open
	} {
		turn := sharedTurn("alice", person, rec)
		if !turn.outsideAudience() {
			t.Fatal("the turn does not read as an outside person's")
		}
		if !turn.readsOwnerFacts() {
			t.Error("the agent's saved notes did not come with it")
		}
		if turn.readsOwnerCortex() {
			t.Error("the owner's cortex reached a person outside")
		}
		if turn.readsOwnerReference() || turn.readsOwnerCorpus(ChunkScopeDerivedOnly) || turn.readsOwnerCorpus(ChunkScopeAll) {
			t.Error("what the agent worked out across the owner's conversations reached a person outside")
		}
		if !turn.readsOwnerCorpus(ChunkScopeCuratedOnly) {
			t.Error("the agent's published documents did not reach a person outside")
		}
	}
	// A recipient who is a gohort user is still governed by the switches.
	if sharedTurn("alice", "bob", AgentRecord{ID: "a1"}).readsOwnerFacts() {
		t.Error("the outside rule reached an ordinary recipient")
	}
}

// Their memory is theirs: the prompt carries their notes over the agent's,
// and nothing they leave lands in the owner's store or another person's.
func TestAnOutsidePersonsNotesStayTheirs(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	ns := factsNamespace("a1")
	StoreMemoryFact(UserDB(root, "alice"), ns, "answer billing questions only")
	ann, ben := agenthost.OutsideScope("key1", "ann"), agenthost.OutsideScope("key1", "ben")
	StoreMemoryFact(UserDB(root, ann), ns, "ann's order number is 4471")

	turn := sharedTurn("alice", ann, AgentRecord{ID: "a1", OutsideAudience: true})
	turn.app, turn.udb = app, UserDB(root, ann)
	got := turn.facts()
	if len(got) != 2 || got[0].Note != "ann's order number is 4471" || got[1].Note != "answer billing questions only" {
		t.Fatalf("ann's prompt facts = %+v; want hers over the agent's", got)
	}

	other := sharedTurn("alice", ben, AgentRecord{ID: "a1", OutsideAudience: true})
	other.app, other.udb = app, UserDB(root, ben)
	for _, f := range other.facts() {
		if f.Note == "ann's order number is 4471" {
			t.Error("one outside person's note reached another")
		}
	}
	for _, f := range ListMemoryFacts(UserDB(root, "alice"), ns) {
		if f.Note == "ann's order number is 4471" {
			t.Error("an outside person's note reached the owner's store")
		}
	}
}
