package orchestrate

// One name is one tool, so bundling a DIFFERENT definition under a name that
// exists rewrites it for everyone who has it. create_agent / update_agent
// inline tools, add_tool and tool_template did that silently, locked tools and
// other agents' tools included.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestBundlingDoesNotSilentlyRewriteAnotherAgentsTool(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"a1", "a2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: "alice", Name: "Agent " + id, OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	v1 := TempTool{Name: "wiki_read", Description: "v1", CommandTemplate: "echo one"}
	v2 := TempTool{Name: "wiki_read", Description: "v2", CommandTemplate: "echo two"}

	// a1 has it alone: a new definition is its to make.
	if err := bundleAgentToolByID(udb, "alice", "a1", v1); err != nil {
		t.Fatal(err)
	}
	if err := bundleAgentToolByID(udb, "alice", "a1", v2); err != nil {
		t.Fatalf("an agent could not update a tool only it has: %v", err)
	}
	// a2 taking the SAME definition shares it.
	if err := bundleAgentToolByID(udb, "alice", "a2", v2); err != nil {
		t.Fatalf("an identical definition should just extend the scope: %v", err)
	}
	// Now shared by two agents: a different definition from either is refused,
	// and says who else would be changed.
	err := bundleAgentToolByID(udb, "alice", "a2", v1)
	if err == nil || !strings.Contains(err.Error(), "Agent a1") {
		t.Fatalf("a different definition rewrote a tool another agent uses: %v", err)
	}
	if got, _ := UserToolByName(udb, "alice", "wiki_read"); got.Tool.Description != "v2" {
		t.Errorf("the refused write still landed: %q", got.Tool.Description)
	}

	// A locked tool is refused a different definition even when it is one
	// agent's alone.
	locked := TempTool{Name: "locked_one", Description: "keep", CommandTemplate: "echo keep", Locked: true}
	if err := bundleAgentToolByID(udb, "alice", "a1", locked); err != nil {
		t.Fatal(err)
	}
	if err := bundleAgentToolByID(udb, "alice", "a1", TempTool{Name: "locked_one", Description: "replaced", CommandTemplate: "echo x"}); err == nil {
		t.Error("a locked tool was rewritten")
	}

	// A new name is held to the creation rules.
	if err := bundleAgentToolByID(udb, "alice", "a1", TempTool{Name: "Bad Name"}); err == nil {
		t.Error("a malformed tool name was accepted")
	}
}
