package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Builder's schedule is the fleet one, the tool that can put the agent it just
// built on a clock, and it stays that tool when it sits behind load_tool. The
// own-clock schedule used to be added beside it under the same name, won the
// deferred index, and refused every "run Jacket Watch each morning" for naming
// an agent, while its other half refused naming none: 34 refusals in one turn.
func TestBuildersDeferredScheduleCanScheduleAnotherAgent(t *testing.T) {
	turn, sess := newAuthoringTestTurn(t)
	turn.agent.ID = "seed-builder"
	sess.Username = "u"
	fleet := operatorManagementToolsFor(sess, turn.agent.ID, turn)
	kept, _ := turn.deferBuilderOnDemandTools(fleet, namesOf(fleet))
	if toolsHaveName(kept, "schedule") || !turn.deferredHas("schedule") {
		t.Fatal("Builder's schedule should wait behind load_tool")
	}
	// What the runner now asks before adding the own-clock schedule.
	if !turn.deferredHas("schedule") {
		t.Fatal("the deferred fleet schedule must count as having one")
	}
	// And if one is offered anyway, the first keeps the name.
	turn.deferOnDemandTools([]AgentToolDef{turn.recurringToolDef()})
	h, ok := turn.lazyToolFallback("schedule")
	if !ok || h == nil {
		t.Fatal("schedule does not resolve")
	}
	_, err := h(context.Background(), map[string]any{"action": "create", "when": "every",
		"agent": "Jacket Watch", "time": "daily 07:00", "then": "check the forecast"})
	if err != nil && strings.Contains(err.Error(), "schedules only its own work") {
		t.Errorf("Builder loaded the own-clock schedule, which cannot run another agent: %v", err)
	}
}
