package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestApprovalAlwaysMeans — the third button appears only where "Always allow"
// does something a plain Approve doesn't. resolveApproval returns before the
// always-branch for the rest, so offering it there would promise a standing
// grant the server never makes.
func TestApprovalAlwaysMeans(t *testing.T) {
	for _, action := range []string{"send_message", "", "delegate"} {
		if !approvalAlwaysMeans(action) {
			t.Errorf("action %q: Always allow is meaningful (pre-authorizes recipient/target)", action)
		}
	}
	for _, action := range []string{"autonomous_tool", "activate_sub_agent", buildAgentAction, "scope_tool", "bind_thread", "converse_contact"} {
		if approvalAlwaysMeans(action) {
			t.Errorf("action %q: resolveApproval returns before the always-branch — don't offer the button", action)
		}
	}
}

// TestApprovalBelongsToAgent — an approval shows in the conversation it came
// from: the agent that raised it, or an agent that owns that one (approving an
// autonomous_tool grants at the parent anyway). A build request follows the
// REQUESTER, which is the thread the user was in.
func TestApprovalBelongsToAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	parent := AgentRecord{ID: "p1", Owner: "u", Name: "Parent", OrchestratorPrompt: "x"}
	child := AgentRecord{ID: "c1", Owner: "u", Name: "Child", OrchestratorPrompt: "x", OwnedBy: "p1"}
	grand := AgentRecord{ID: "g1", Owner: "u", Name: "Grandchild", OrchestratorPrompt: "x", OwnedBy: "c1"}
	other := AgentRecord{ID: "o1", Owner: "u", Name: "Unrelated", OrchestratorPrompt: "x"}
	for _, rec := range []AgentRecord{parent, child, grand, other} {
		if _, err := saveAgent(db, rec); err != nil {
			t.Fatalf("saveAgent %s: %v", rec.ID, err)
		}
	}

	cases := []struct {
		name    string
		auth    Authorization
		agentID string
		want    bool
	}{
		{"own request", Authorization{Action: "autonomous_tool", Agent: "p1"}, "p1", true},
		{"sub-agent's request shows on the parent", Authorization{Action: "autonomous_tool", Agent: "c1"}, "p1", true},
		{"walks the whole chain", Authorization{Action: "autonomous_tool", Agent: "g1"}, "p1", true},
		{"not the parent's business downward", Authorization{Action: "autonomous_tool", Agent: "p1"}, "c1", false},
		{"unrelated agent", Authorization{Action: "autonomous_tool", Agent: "o1"}, "p1", false},
		{"build request follows the requester", Authorization{Action: buildAgentAction, Agent: "builder", FromAgent: "p1"}, "p1", true},
		{"build request not shown to the builder", Authorization{Action: buildAgentAction, Agent: "builder", FromAgent: "p1"}, "builder", false},
		{"no agent at all", Authorization{Action: "autonomous_tool"}, "p1", false},
	}
	for _, tc := range cases {
		if got := approvalBelongsToAgent(db, tc.auth, tc.agentID); got != tc.want {
			t.Errorf("%s: belongs=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestApprovalDisplayCoversQueuedActions — the card's copy comes from the same
// labeler the Permissions pane uses. autonomous_tool and scope_tool used to
// fall through to the raw agent id + bare tool name, which reads as noise in a
// conversation.
func TestApprovalDisplayCoversQueuedActions(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if _, err := saveAgent(db, AgentRecord{ID: "a1", Owner: "u", Name: "Daily Entertainment", OrchestratorPrompt: "x"}); err != nil {
		t.Fatalf("saveAgent: %v", err)
	}
	who, detail := approvalDisplay(db, "u", Authorization{Action: "autonomous_tool", Agent: "a1", Brief: "send_message"})
	if who != "Daily Entertainment" {
		t.Errorf("who = %q, want the agent's name", who)
	}
	if detail == "send_message" || detail == "" {
		t.Errorf("detail = %q, want a sentence explaining what approving does", detail)
	}
	who, detail = approvalDisplay(db, "u", Authorization{Action: "scope_tool", Agent: "a1", Brief: "get_meme"})
	if who != "Daily Entertainment" || detail == "get_meme" {
		t.Errorf("scope_tool display = %q / %q", who, detail)
	}
}
