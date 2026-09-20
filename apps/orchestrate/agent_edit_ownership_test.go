package orchestrate

// A shared agent is somebody else's record. You run it; you do not change it.
//
// The HTTP editor and agent_crud_tools both said so already. The tools that
// attach something TO an agent by name did not: each resolved its target with
// the shared-agent fallback and wrote, which put a fork of somebody else's
// agent in the caller's own store — shadowing the shared record from then on,
// so it stopped following the owner's edits while everything on screen still
// said it was theirs.

import (
	"os"
	"strings"
	"testing"
)

func TestSomebodyElsesAgentIsNotYoursToChange(t *testing.T) {
	shared := AgentRecord{ID: "a1", Name: "Troubleshooter", Owner: "alice"}
	if msg := agentEditRefusal(shared, "bob"); msg == "" {
		t.Error("a recipient may edit the agent that was shared with them")
	} else {
		// It has to say whose it is and what to do instead, or the model
		// tries the same call again with a different spelling.
		for _, want := range []string{"alice", "Duplicate"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the refusal does not mention %q: %s", want, msg)
			}
		}
	}
	if msg := agentEditRefusal(shared, "alice"); msg != "" {
		t.Errorf("the owner was refused their own agent: %s", msg)
	}
}

// A framework seed is shadow-cloned per user and an unowned record predates
// ownership. Neither is another user's, and both must keep behaving as before.
func TestSeedsAndUnownedRecordsAreUnaffected(t *testing.T) {
	if msg := agentEditRefusal(AgentRecord{ID: "s", Name: "Operator", Owner: seedOwner}, "bob"); msg != "" {
		t.Errorf("a framework seed was refused: %s", msg)
	}
	if msg := agentEditRefusal(AgentRecord{ID: "old", Name: "Legacy"}, "bob"); msg != "" {
		t.Errorf("an unowned record was refused: %s", msg)
	}
}

// Every tool that resolves an agent with the SHARED fallback and then writes
// has to consult the guard. Structural, because the bug is a missing call: a
// new attach-to-agent tool written next year would reintroduce it, and no
// behavioural test of the tools that exist today would notice.
func TestEveryAttachToolChecksOwnership(t *testing.T) {
	for _, file := range []string{
		"skill_def_tool.go", "machine_def_tool.go", "pipeline_def_tool.go", "add_tool.go",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		src := string(raw)
		if !strings.Contains(src, "findAgentByNameOrID(") {
			continue // no longer resolves an agent at all
		}
		if !strings.Contains(src, "agentEditRefusal(") {
			t.Errorf("%s resolves an agent by name (which finds shared ones) and never checks whether it is the caller's to change", file)
		}
	}
}
