package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A scoped tool is attached by intent, which is why it bypasses the
// allowed_tools gate — but "attached" and "in use right now" are different
// statements, and until the Tools modal grew a checkbox for it the second had
// no off switch at all. Unchecking one writes the name into the agent's deny
// list; this is the runtime end of that: the tool stops loading, without being
// detached and without becoming unfixable by Builder.
func TestScopedToolHonorsPerAgentOptOut(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	mk := func(name string) TempTool {
		return TempTool{Name: name, Description: "d", CommandTemplate: "echo hi"}
	}
	for _, n := range []string{"kept_tool", "muted_tool"} {
		if err := AdminPersistTempTool(udb, "u", mk(n)); err != nil {
			t.Fatal(err)
		}
		if !SetUserToolScopeAgents(udb, "u", n, []string{"agent-1"}) {
			t.Fatalf("scope write failed for %s", n)
		}
	}

	load := func(agentID string) map[string]bool {
		turn := &chatTurn{
			user: "u", udb: udb,
			agent: AgentRecord{
				ID: agentID, Owner: "u",
				DisabledPersistentTools: []string{"muted_tool"},
			},
		}
		sess := &ToolSession{}
		turn.loadAgentTempTools(sess, "u", root)
		got := map[string]bool{}
		for _, tt := range sess.TempTools {
			got[tt.Name] = true
		}
		return got
	}

	got := load("agent-1")
	if !got["kept_tool"] {
		t.Error("a scoped tool with no opt-out must still load")
	}
	if got["muted_tool"] {
		t.Error("an unchecked scoped tool must not load for the agent that unchecked it")
	}

	// Builder is the surface that repairs tools. A tool it could not run is a
	// tool it could not fix, so the opt-out must not reach it.
	got = load("seed-builder")
	if !got["muted_tool"] {
		t.Error("Builder loads every tool it can edit, including muted ones")
	}
}
