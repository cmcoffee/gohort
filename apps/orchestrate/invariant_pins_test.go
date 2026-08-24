package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Claims that HOLD today with nothing pinning them. Each is one assertion, and
// each guards something a future edit would break silently — the population the
// 2026-08-23 parity sweep found its twenty violations in.

// appendAgentCapabilityBlocks is documented as THE place the per-agent
// capability blocks live, so that a new block reaches the web turn and the
// channel/dispatch turn at once. Two callers, and a third prompt-building
// surface added later would inherit nothing.
func TestCapabilityBlocksHaveExactlyTheTwoKnownCallers(t *testing.T) {
	src, err := readRunnerSource()
	if err != nil {
		t.Skip("runner source unavailable")
	}
	// Deliberately a source count and nothing more: what must not change is how
	// MANY surfaces assemble this, and a behavioural test cannot see a caller
	// that does not exist yet. Kept honest by asserting the exact number rather
	// than a floor — the failure mode is a third surface built by hand.
	if n := strings.Count(src, "appendAgentCapabilityBlocks("); n != 3 {
		t.Errorf("appendAgentCapabilityBlocks appears %d times in runner.go (want 3: its definition, the web turn, the dispatch prompt). A new prompt-building surface must call it, not rebuild its blocks.", n)
	}
}

// Every machine-delete path must detach the machine from its agents first, or
// an agent keeps a dangling reference to a machine that no longer exists.
func TestEveryMachineDeleteDetachesFirst(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	SaveMachineDef(udb, MachineDef{ID: "m1", Name: "Triage", Owner: "u"})
	agent, err := saveAgent(udb, AgentRecord{Name: "Holder", Owner: "u", OrchestratorPrompt: "p", Machine: "m1"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	detached := detachMachineFromAgents(udb, "u", "m1")
	if len(detached) == 0 {
		t.Fatal("detachMachineFromAgents reported nothing; the fixture agent should have been holding the machine")
	}
	reloaded, ok := loadAgent(udb, agent.ID)
	if !ok {
		t.Fatal("agent vanished")
	}
	if reloaded.Machine == "m1" {
		t.Error("agent still references the deleted machine — a delete path that skips detaching leaves exactly this")
	}
}

// Locked is the human's lock icon, and saveAgent now preserves the stored flag
// for every caller. This could not be pinned before: the rule lived in three
// HTTP save paths and in nothing beneath them, so a test at the saveAgent layer
// FAILED against correct code — which is what the first version of this test
// did. The rule moved into the write path, so the test is now possible AND the
// fourth save path that nobody has written yet is covered.
func TestSaveAgentPreservesLocked(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	rec, err := saveAgent(udb, AgentRecord{Name: "Pinned", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := setAgentLocked(udb, rec, true); err != nil {
		t.Fatalf("lock: %v", err)
	}

	// An ordinary edit carrying Locked=false — what a partial POST decoding
	// into a zero value looks like — must not unlock it.
	edit := rec
	edit.Description = "edited"
	edit.Locked = false
	if _, err := saveAgent(udb, edit); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, ok := loadAgent(udb, rec.ID)
	if !ok {
		t.Fatal("agent vanished")
	}
	if !got.Locked {
		t.Fatal("an ordinary save cleared Locked; only setAgentLocked may write that field")
	}
	if got.Description != "edited" {
		t.Errorf("the edit itself did not land: Description = %q", got.Description)
	}

	// And the one door still opens both ways, or the icon cannot unlock.
	unlocked, err := setAgentLocked(udb, got, false)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if unlocked.Locked {
		t.Error("setAgentLocked(false) returned a record still marked locked — the handler reports this value to the browser")
	}
	if reloaded, _ := loadAgent(udb, rec.ID); reloaded.Locked {
		t.Error("setAgentLocked(false) did not persist the unlock")
	}

	// A creation may still arrive locked=false without a stored record to
	// preserve from; nothing to restore, and the caller's value stands.
	fresh, err := saveAgent(udb, AgentRecord{Name: "Fresh", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if fresh.Locked {
		t.Error("a newly created agent came out locked")
	}
}
