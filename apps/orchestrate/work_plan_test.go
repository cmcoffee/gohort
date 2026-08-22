// The tracked plan, mounted on a conversation.
//
// Two things make it different from the turn-level fan-out it replaces: it
// SURVIVES the turn, and it is drawn while it runs. Both are tested here,
// because both were the reason the earlier plan object (servitor's, now core's)
// could not be used anywhere else.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func workPlanTurn(t *testing.T) *chatTurn {
	t.Helper()
	_, udb, user := newTestOrchestrate(t)
	ag := AgentRecord{ID: "ag-wp", Name: "Wren", Owner: user, OrchestratorPrompt: "x", WorkPlan: true}
	if _, err := saveAgent(udb, ag); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	stored, _ := loadAgent(udb, "ag-wp")
	sess := ChatSession{ID: "s-wp", AgentID: "ag-wp"}
	if saved, err := saveChatSession(udb, sess); err == nil {
		sess = saved
	}
	return &chatTurn{udb: udb, user: user, agent: stored, session: &sess}
}

// A plan set in one message has to still be the plan in the next, or it is a
// card rather than a commitment.
func TestATrackedPlanSurvivesTheTurn(t *testing.T) {
	turn := workPlanTurn(t)
	tools := turn.workPlanTools()
	if len(tools) != 6 {
		t.Fatalf("an agent with a tracked plan should hold the six plan tools, got %d", len(tools))
	}
	if _, err := turn.workPlan.Set.Handler(map[string]any{"steps": []any{
		map[string]any{"title": "read the logs", "what_to_find": "what failed"},
		map[string]any{"title": "check the config", "what_to_find": "whether it is set"},
	}}); err != nil {
		t.Fatalf("set_plan: %v", err)
	}
	if _, err := turn.workPlan.Findings.Handler(map[string]any{"step_id": 1, "findings": "the disk filled"}); err != nil {
		t.Fatal(err)
	}
	// A LATER turn, built fresh from the stored session — the way the next
	// message arrives.
	later, ok := loadChatSession(turn.udb, "ag-wp", "s-wp")
	if !ok {
		t.Fatal("the session should have been saved")
	}
	if later.WorkPlan == nil || !later.WorkPlan.IsSet() {
		t.Fatal("the plan did not survive the turn — it is a card, not a commitment")
	}
	steps := later.WorkPlan.Snapshot()
	if len(steps) != 2 || steps[0].Status != WorkStepDone || steps[0].Findings != "the disk filled" {
		t.Fatalf("the plan came back without its progress: %+v", steps)
	}
	if later.WorkPlan.Pending() != 1 {
		t.Errorf("one step is still to do; got %d pending", later.WorkPlan.Pending())
	}
}

// An agent without the flag keeps plan_set and gets no group: the two are
// alternatives, not layers.
func TestNoTrackedPlanWithoutTheFlag(t *testing.T) {
	turn := workPlanTurn(t)
	turn.agent.WorkPlan = false
	if got := turn.workPlanTools(); got != nil {
		t.Errorf("an agent that did not opt in should hold no plan tools, got %d", len(got))
	}
	// And the framework's plan_set section is gated on the same flag, so an
	// agent that HAS opted in is never told to reach for a tool it lacks.
	on := AgentRecord{ID: "a", Name: "A", WorkPlan: true, OrchestratorPrompt: "x"}
	if blocks := frameworkPromptBlocks("", on, true); strings.Contains(blocks, planSetSectionHeading) {
		t.Error("an agent with a tracked plan was still handed the plan_set section")
	}
	off := AgentRecord{ID: "b", Name: "B", OrchestratorPrompt: "x"}
	if blocks := frameworkPromptBlocks("", off, true); !strings.Contains(blocks, planSetSectionHeading) {
		t.Error("an ordinary agent lost the plan_set section")
	}
}
