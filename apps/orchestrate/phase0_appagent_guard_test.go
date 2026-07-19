package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Phase 0 of the AgentRecord split: app agents can't hold LLM-authored tools and
// aren't scope targets — enforced by IDENTITY (the appagents registry), not the
// drift-able Hidden/Owner proxies. A VISIBLE app agent (Hidden:false) is the
// case the old proxies leaked, so these register one and assert it's still
// walled off.

const phase0AppID = "test-phase0-appagent-guard"

func init() {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: phase0AppID, Name: "Phase0App", Prompt: "p", Hidden: false, // deliberately VISIBLE
	})
}

func TestBundleAgentToolRefusesAppAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	base := AgentRecord{ID: phase0AppID, Name: "Phase0App"}
	err := bundleAgentTool(db, "alice", base, TempTool{Name: "ts3_list_clients"})
	if err == nil || !strings.Contains(err.Error(), "app agent") {
		t.Fatalf("bundleAgentTool must refuse an app agent; got %v", err)
	}
}

func TestBundleAgentToolByIDRefusesAppAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := agentUserDB(root, "alice")
	err := bundleAgentToolByID(udb, "alice", phase0AppID, TempTool{Name: "ts3_list_clients"})
	if err == nil || !strings.Contains(err.Error(), "app agent") {
		t.Fatalf("bundleAgentToolByID must refuse an app agent; got %v", err)
	}
}

// Removal must stay OPEN so an already-mis-scoped tool can be cleaned off an app
// agent — only the write (bundle) path is guarded, not unbundle.
func TestUnbundleFromAppAgentStillWorks(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := agentUserDB(root, "alice")
	// Seed a stuck tool directly onto the app agent's shadow (as the old bug did).
	if _, err := saveAgent(udb, AgentRecord{
		ID: phase0AppID, Name: "Phase0App", OrchestratorPrompt: "p", Owner: seedOwner,
		Tools: []TempTool{{Name: "ts3_list_clients"}},
	}); err != nil {
		t.Fatalf("seed stuck tool: %v", err)
	}
	if err := unbundleAgentToolByID(udb, "alice", phase0AppID, "ts3_list_clients"); err != nil {
		t.Fatalf("must be able to strip a mis-scoped tool off an app agent; got %v", err)
	}
}

func TestToolScopePillExcludesVisibleAppAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	user, err := saveAgent(udb, AgentRecord{Name: "Mine", OrchestratorPrompt: "p", Owner: owner})
	if err != nil {
		t.Fatalf("save user agent: %v", err)
	}
	if err := AdminPersistTempTool(root, owner, TempTool{Name: "gtool"}); err != nil {
		t.Fatalf("persist global tool: %v", err)
	}

	st, ok := toolScopeState(root, owner, "gtool")
	if !ok {
		t.Fatal("expected scope state for a global tool")
	}
	sawUser, sawApp := false, false
	for _, ag := range st.Agents {
		if ag.ID == user.ID {
			sawUser = true
		}
		if ag.ID == phase0AppID {
			sawApp = true
		}
	}
	if sawApp {
		t.Fatal("a VISIBLE app agent must NOT be a tool-scope pill target (identity exclusion)")
	}
	if !sawUser {
		t.Fatal("the user's own agent should be a scope target")
	}
}
