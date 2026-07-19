package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestUnbundleAgentToolByIDStripsSeedOwnedAgent reproduces the "Case Analyzer
// keeps coming back" bug: a tool mis-scoped onto an APP agent (Owner=seedOwner,
// not the human) could never be removed, because the admin scope path used
// unbundleAgentTool — whose rec.Owner!=owner guard rejects seed/app/sub agents
// with "not your agent". That made the Access-selector unselect fail and made
// promoteScopedToGlobal's strip silently no-op, so the scoped copy survived and
// resurfaced. unbundleAgentToolByID (the admin-scope twin, guard-free) must
// strip it. The runtime-guarded unbundleAgentTool is asserted to still reject,
// so we don't accidentally drop the guard where it's intended.
func TestUnbundleAgentToolByIDStripsSeedOwnedAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)

	// An app-agent-shaped record: Owner is the framework (seedOwner), not the
	// human — exactly like Casefile's "Case Analyzer" resolves.
	rec, err := saveAgent(udb, AgentRecord{
		Name:               "Case Analyzer",
		OrchestratorPrompt: "p",
		Owner:              seedOwner,
		Tools:              []TempTool{{Name: "ts3_list_clients"}},
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	// The guarded runtime path rejects it — documents why the bug existed.
	if err := unbundleAgentTool(udb, owner, rec.ID, "ts3_list_clients"); err == nil {
		t.Fatal("expected unbundleAgentTool to reject a seed-owned agent (the guard); it didn't")
	}

	// The admin-scope path must succeed and actually remove the tool.
	if err := unbundleAgentToolByID(udb, owner, rec.ID, "ts3_list_clients"); err != nil {
		t.Fatalf("unbundleAgentToolByID must strip the mis-scoped tool, got: %v", err)
	}
	got, ok := loadAgent(udb, rec.ID)
	if !ok {
		t.Fatal("agent vanished after unbundle")
	}
	for _, tl := range got.Tools {
		if tl.Name == "ts3_list_clients" {
			t.Fatal("tool still bundled after unbundleAgentToolByID — it would resurrect")
		}
	}

	// Idempotent: a second strip reports not-bundled rather than succeeding
	// silently (so a caller can tell the difference).
	if err := unbundleAgentToolByID(udb, owner, rec.ID, "ts3_list_clients"); err == nil {
		t.Fatal("expected not-bundled error on second strip")
	}
}
