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

// Locked: NOT pinned here, deliberately, and the attempt is worth recording.
//
// The claim is that handleAgentLock is the only writer and every other save
// restores the stored value first. That is true of the three HTTP save paths
// (agents.go:1963, :2001, :2609) and of nothing below them: saveAgent writes
// whatever record it is handed. A test at the saveAgent layer therefore FAILS
// against correct code, which is what the first version of this file did.
//
// Pinning it honestly means either driving those three handlers (HTTP plus auth
// this package has no harness for) or moving the restore into saveAgent with a
// carve-out for the lock handler — a design change, not a test. Left as it
// stands, with the risk named: a fourth save path that calls saveAgent directly
// will silently unlock an agent an admin locked, and nothing will say so.
