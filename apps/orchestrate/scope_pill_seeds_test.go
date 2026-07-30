package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Framework seeds are retired from the scope-pill surfaces, and the exclusion
// has to be by IDENTITY. A seed the user customized exists as a SHADOW — the
// seed's ID with the user's Owner — and the old Owner==seedOwner test let every
// shadow through, which is how "Chat" kept turning up in the pills after its
// retirement while Builder (correctly excluded, invisibly) did not.
func TestScopePillsRetireSeedsByIdentity(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	// toolScopeState resolves agents via agentUserDB = UserDB(root.Bucket(
	// "orchestrate"), owner) — mirror that store here or the saved agents are
	// invisible to it.
	udb := agentUserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	// A user agent, and a seed-chat SHADOW: seed ID, user Owner — the shape a
	// customized seed takes, and the one the ownership test missed.
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-real", Name: "Wren", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "seed-chat", Name: "Chat", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "shared_tool", Description: "d", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}

	st, found := toolScopeState(root, "u", "shared_tool")
	if !found {
		t.Fatal("tool not found")
	}
	byID := map[string]bool{}
	for _, a := range st.Agents {
		byID[a.ID] = true
	}
	if !byID["agent-real"] {
		t.Error("a real user agent must be offered as a target")
	}
	if byID["seed-chat"] {
		t.Error("a retired seed's SHADOW must not be offered — identity, not ownership")
	}
	if byID["seed-builder"] {
		t.Error("Builder must never be a pill — it reads the whole pool by identity")
	}

	// The revocability exception: a seed that already HOLDS a grant stays
	// listed, so the grant can be turned off — and then it leaves.
	if !SetUserToolScopeAgents(udb, "u", "shared_tool", []string{"seed-chat"}) {
		t.Fatal("scope write failed")
	}
	st, _ = toolScopeState(root, "u", "shared_tool")
	held := false
	for _, a := range st.Agents {
		if a.ID == "seed-chat" && a.On {
			held = true
		}
	}
	if !held {
		t.Error("a seed holding an explicit grant must stay visible and ON, or the grant can never be revoked")
	}
}
