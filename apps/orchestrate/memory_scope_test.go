package orchestrate

// Whose memory, versus whose run.
//
// One identity answered both until a shared agent made them different people.
// The run stays the recipient's; the memory is theirs stacked over the
// owner's. Every test here is about one of three things: the axes stay apart,
// the defaults match what the framework already did, and nothing a recipient
// does reaches the owner's layers.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func sharedTurn(owner, runBy string, a AgentRecord) *chatTurn {
	a.Owner = owner
	return &chatTurn{agent: a, user: runBy, ownerUser: owner}
}

// The whole point: the run is the recipient's even where the memory is not.
// A run filed under the owner would vanish from the recipient's own feed,
// which is the failure class this codebase least tolerates.
func TestTheRunStaysTheRunnersWhateverTheMemoryDoes(t *testing.T) {
	ms := sharedTurn("alice", "bob", AgentRecord{ID: "a1"}).memoryScope()
	if ms.Run != "bob" {
		t.Errorf("the run was filed under %q; it is bob's run", ms.Run)
	}
	if ms.Write != "bob" {
		t.Errorf("writes were pointed at %q; what bob's turn learns is bob's", ms.Write)
	}
	if len(ms.Under) != 1 || ms.Under[0] != "alice" {
		t.Errorf("the owner's layers are not underneath: %+v", ms.Under)
	}
	// The owner's own run stacks nothing under itself.
	own := sharedTurn("alice", "alice", AgentRecord{ID: "a1"}).memoryScope()
	if len(own.Under) != 0 {
		t.Errorf("the owner got their own memory twice: %+v", own.Under)
	}
}

// The defaults ARE the behaviour that already shipped, and it is not the same
// answer for all three. Getting this wrong would silently revoke two layers on
// every share that works today.
func TestTheDefaultsAreWhatTheFrameworkAlreadyDid(t *testing.T) {
	turn := sharedTurn("alice", "bob", AgentRecord{ID: "a1", Cortex: true})
	if !turn.readsOwnerCortex() {
		t.Error("the cortex already travelled; the default must keep it travelling")
	}
	if !turn.readsOwnerReference() {
		t.Error("the owner's corpus already travelled; the default must keep it travelling")
	}
	if turn.readsOwnerFacts() {
		t.Error("saved facts have never travelled; the default must not start")
	}
}

// Each switch moves exactly one layer.
func TestEachSwitchMovesOneLayer(t *testing.T) {
	cases := []struct {
		name                     string
		rec                      AgentRecord
		cortex, reference, facts bool
	}{
		{"hold the cortex", AgentRecord{ID: "a", ShareHoldCortex: true}, false, true, false},
		{"hold what it worked out", AgentRecord{ID: "a", ShareHoldReference: true}, true, false, false},
		{"grant the saved notes", AgentRecord{ID: "a", ShareMemoryExplicit: true}, true, true, true},
	}
	for _, c := range cases {
		turn := sharedTurn("alice", "bob", c.rec)
		if got := turn.readsOwnerCortex(); got != c.cortex {
			t.Errorf("%s: cortex = %v, want %v", c.name, got, c.cortex)
		}
		if got := turn.readsOwnerReference(); got != c.reference {
			t.Errorf("%s: reference = %v, want %v", c.name, got, c.reference)
		}
		if got := turn.readsOwnerFacts(); got != c.facts {
			t.Errorf("%s: facts = %v, want %v", c.name, got, c.facts)
		}
	}
}

// Documents somebody uploaded on purpose are knowledge and travel with the
// agent the way its collections do. What the agent inferred for itself is
// memory and is gated. The two rode one argument until a switch over the
// second silently withheld the first.
func TestKnowledgeTravelsEvenWhenMemoryIsHeld(t *testing.T) {
	turn := sharedTurn("alice", "bob", AgentRecord{ID: "a1", ShareHoldReference: true})
	if !turn.readsOwnerCorpus(ChunkScopeCuratedOnly) {
		t.Error("holding back the inferred layer also withheld uploaded documents")
	}
	if turn.readsOwnerCorpus(ChunkScopeDerivedOnly) {
		t.Error("the inferred layer was searched although it is held")
	}
	if turn.readsOwnerCorpus(ChunkScopeAll) {
		t.Error("an all-scopes search reached the held layer")
	}
	// With nothing held, every scope reaches it.
	open := sharedTurn("alice", "bob", AgentRecord{ID: "a1"})
	for _, sc := range []ChunkScope{ChunkScopeAll, ChunkScopeCuratedOnly, ChunkScopeDerivedOnly} {
		if !open.readsOwnerCorpus(sc) {
			t.Errorf("scope %v does not reach the owner's corpus by default", sc)
		}
	}
}

// A channel inbound runs under a synthetic per-chat identity whose own
// namespace is blank by construction. It reads the owner's layers, and must:
// an agent put on a channel with no access to what it knows is the useless
// version of itself, and putting it there was the decision to let that channel
// reach it.
//
// What is new is that the switches govern it there too. Before this there was
// no way to put an agent on a channel and keep its saved notes off it.
func TestAChannelTurnReadsTheOwnerButObeysTheSwitches(t *testing.T) {
	turn := sharedTurn("alice", "phantom:9921", AgentRecord{ID: "a1", Cortex: true})
	if turn.memoryUnderlay() != "alice" {
		t.Fatalf("a channel run lost the agent's own memory: %q", turn.memoryUnderlay())
	}
	if !turn.readsOwnerCortex() {
		t.Error("a channel run cannot see the agent's standing activity")
	}
	held := sharedTurn("alice", "phantom:9921", AgentRecord{ID: "a1", Cortex: true, ShareHoldCortex: true})
	if held.readsOwnerCortex() {
		t.Error("the switch does not reach a channel run")
	}
}

// A seed belongs to the framework, which has no namespace of its own to read.
func TestASeedHasNoOwnerToReadFrom(t *testing.T) {
	turn := sharedTurn(seedOwner, "bob", AgentRecord{ID: "seed-research"})
	if turn.memoryUnderlay() != "" {
		t.Error("a seed agent was given the framework marker as a memory namespace")
	}
}

// A clean room takes nothing in. That rule already governed the runner's own
// layers; it has to govern the owner's too, or incognito on a shared agent
// would be the leakiest session on the surface.
func TestACleanRoomTakesNothingFromTheOwnerEither(t *testing.T) {
	turn := sharedTurn("alice", "bob", AgentRecord{ID: "a1", ShareMemoryExplicit: true})
	turn.session = &ChatSession{ID: "s1", Incognito: true}
	if turn.memoryUnderlay() != "" {
		t.Fatal("an incognito session still stacked the owner's memory underneath")
	}
	if turn.readsOwnerCortex() || turn.readsOwnerReference() || turn.readsOwnerFacts() {
		t.Error("a clean room read one of the owner's layers")
	}
}

// The owner's facts sit UNDER the runner's, so where the two disagree the
// person actually in the conversation has the last word.
func TestTheRunnersOwnFactsComeFirst(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	ns := factsNamespace("a1")
	StoreMemoryFact(UserDB(root, "alice"), ns, "the deploy window is Tuesday")
	StoreMemoryFact(UserDB(root, "bob"), ns, "bob prefers short answers")

	turn := sharedTurn("alice", "bob", AgentRecord{ID: "a1", ShareMemoryExplicit: true})
	turn.app, turn.udb = app, UserDB(root, "bob")
	got := turn.facts()
	if len(got) != 2 {
		t.Fatalf("want both layers, got %d: %+v", len(got), got)
	}
	if got[0].Note != "bob prefers short answers" {
		t.Errorf("the owner's notes were put above the runner's: %q first", got[0].Note)
	}

	// And with the switch off, the owner's are simply not there.
	turn.agent.ShareMemoryExplicit = false
	if got := turn.facts(); len(got) != 1 {
		t.Errorf("the owner's facts travelled with the switch off: %+v", got)
	}
}
