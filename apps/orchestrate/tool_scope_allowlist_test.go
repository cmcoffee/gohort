package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestDisableGlobalToolForAgentLastEntryDoesNotWiden pins the access-widening
// bug in disableGlobalToolForAgent: an agent whose AllowedTools holds exactly
// one tool, when that tool's per-agent pill is toggled OFF, must NOT end up
// with an empty AllowedTools. Empty reads as "sees the whole default pool"
// (agentSeesGlobalTool; agents.go:540), so emptying the list would flip the
// agent from ONE tool to EVERY tool — the opposite of what the admin asked
// for, and reachable from both the admin and in-chat pill UIs. The correct
// end state is the explicit no-tools sentinel.
func TestDisableGlobalToolForAgentLastEntryDoesNotWiden(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}

	saved, err := saveAgent(udb, AgentRecord{
		Name:               "Narrow",
		OrchestratorPrompt: "pinned to exactly one tool",
		Owner:              owner,
		AllowedTools:       []string{"from_client.only_tool"},
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}

	if err := disableGlobalToolForAgent(udb, owner, saved.ID, "from_client.only_tool"); err != nil {
		t.Fatalf("disableGlobalToolForAgent: %v", err)
	}

	rec, ok := loadAgent(udb, saved.ID)
	if !ok {
		t.Fatal("loadAgent after disable failed")
	}
	if len(rec.AllowedTools) == 0 {
		t.Fatal("AllowedTools emptied — agent now sees the ENTIRE tool pool instead of none (access-widening regression)")
	}
	if !isNoToolsSentinel(rec.AllowedTools) {
		t.Fatalf("AllowedTools = %v; want the no-tools sentinel %q", rec.AllowedTools, noToolsSentinel)
	}
	// The whole point: the agent must not see a global tool it never allowed.
	if agentSeesGlobalTool(rec, "from_client.some_other_tool") {
		t.Fatal("agent sees an unrelated global tool after its only allowed tool was turned off")
	}
}

// TestSelfHealAllowedToolsLastEntryDoesNotWiden pins the same access-widening
// hazard on the self-heal path: an agent pinned to a restricted list whose
// entries have ALL gone stale (e.g. the only temp tool it named was deleted)
// must collapse to the no-tools sentinel, not to an empty list — empty reads as
// "sees the whole default pool", so the deliberate restriction would silently
// invert into unrestricted access.
func TestSelfHealAllowedToolsLastEntryDoesNotWiden(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}

	// "ghost_tool" resolves to nothing: not registered, not a from_client.
	// bridge tool, not in the owner's temp-tool pool. Self-heal must drop it.
	saved, err := saveAgent(udb, AgentRecord{
		Name:               "Stale",
		OrchestratorPrompt: "its only tool no longer exists",
		Owner:              owner,
		AllowedTools:       []string{"ghost_tool"},
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}

	// loadAgent runs the self-heal.
	rec, ok := loadAgent(udb, saved.ID)
	if !ok {
		t.Fatal("loadAgent failed")
	}
	if len(rec.AllowedTools) == 0 {
		t.Fatal("AllowedTools healed to empty — agent silently widened to the ENTIRE default tool pool")
	}
	if !isNoToolsSentinel(rec.AllowedTools) {
		t.Fatalf("AllowedTools = %v; want the no-tools sentinel %q", rec.AllowedTools, noToolsSentinel)
	}
	if agentSeesGlobalTool(rec, "from_client.anything") {
		t.Fatal("agent sees a global tool after every allowed tool healed away")
	}
}

// TestDisableGlobalToolForAgentKeepsSiblings is the non-regression half: with
// more than one entry, removing one must leave the rest intact and must NOT
// substitute the sentinel.
func TestDisableGlobalToolForAgentKeepsSiblings(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	if udb == nil {
		t.Fatal("agentUserDB nil")
	}

	saved, err := saveAgent(udb, AgentRecord{
		Name:               "Wide",
		OrchestratorPrompt: "several tools",
		Owner:              owner,
		AllowedTools:       []string{"from_client.keep_a", "from_client.drop_me", "from_client.keep_b"},
	})
	if err != nil {
		t.Fatalf("saveAgent: %v", err)
	}

	if err := disableGlobalToolForAgent(udb, owner, saved.ID, "from_client.drop_me"); err != nil {
		t.Fatalf("disableGlobalToolForAgent: %v", err)
	}

	rec, ok := loadAgent(udb, saved.ID)
	if !ok {
		t.Fatal("loadAgent after disable failed")
	}
	if isNoToolsSentinel(rec.AllowedTools) {
		t.Fatal("sentinel written despite surviving entries")
	}
	if len(rec.AllowedTools) != 2 {
		t.Fatalf("AllowedTools = %v; want the two keepers", rec.AllowedTools)
	}
	for _, n := range rec.AllowedTools {
		if n == "from_client.drop_me" {
			t.Fatalf("drop_me still present: %v", rec.AllowedTools)
		}
	}
}
