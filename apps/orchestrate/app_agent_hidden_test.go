package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAppAgentHiddenRefreshesFromSpec pins the "Case Analyzer still shows as a
// pill after we hid it" fix. App-agents resolve through loadAgent's seed
// branch; when a shadow record exists (created the moment a tool got mis-scoped
// onto the app-agent via the bundle path's saveAgent) it becomes the base, and
// loadAgent must refresh Hidden from the SPEC — the app owns visibility, not a
// stale shadow. Without the refresh, flipping the spec to Hidden:true never
// takes and the agent keeps appearing in the picker + scope pills, unlike
// Servitor / Guide Author which have no shadow.
func TestAppAgentHiddenRefreshesFromSpec(t *testing.T) {
	const id = "test-apphidden-refresh-xyz"
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: id, Name: "TestHiddenRefresh", Prompt: "p", Hidden: true,
	})
	db := &DBase{Store: kvlite.MemStore()}

	// The stale shadow the bundle path leaves behind: Hidden=false persisted,
	// with a tool bundled on — exactly Case Analyzer's state.
	if _, err := saveAgent(db, AgentRecord{
		ID: id, Name: "TestHiddenRefresh", OrchestratorPrompt: "p",
		Owner: seedOwner, Hidden: false,
		Tools: []TempTool{{Name: "ts3_list_clients"}},
	}); err != nil {
		t.Fatalf("save shadow: %v", err)
	}

	got, ok := loadAgent(db, id)
	if !ok {
		t.Fatal("app-agent not resolved")
	}
	if !got.Hidden {
		t.Fatal("app-agent Hidden must refresh from the spec (true) despite a stale shadow (false) — else it keeps showing in the picker + scope pills")
	}

	// scopeTarget (the pill filter) must now exclude it.
	if scopeTargetAgent(got) {
		t.Fatal("a hidden app-agent must not be a scope-pill target")
	}
}

// scopeTargetAgent mirrors toolScopeState's scopeTarget predicate so the test
// asserts the pill-filtering outcome directly.
func scopeTargetAgent(a AgentRecord) bool { return !a.Hidden && a.OwnedBy == "" }
