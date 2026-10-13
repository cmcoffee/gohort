package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func presentPlan(t *testing.T, turn *chatTurn, titles ...string) {
	t.Helper()
	var steps []any
	for _, s := range titles {
		steps = append(steps, map[string]any{"title": s})
	}
	if _, err := turn.presentBuildPlanToolDef().Handler(context.Background(), map[string]any{"steps": steps}); err != nil {
		t.Fatalf("present_build_plan: %v", err)
	}
}

func markDone(t *testing.T, turn *chatTurn, step int) {
	t.Helper()
	if _, err := turn.markStepDoneToolDef().Handler(context.Background(), map[string]any{"step": float64(step), "summary": "ok"}); err != nil {
		t.Fatalf("mark_step_done: %v", err)
	}
}

// A plan card is kept with the thread and stamped when first drawn, so a
// reload puts it back where it was rather than after every message. Observed:
// a finished plan reappeared at the end of the conversation.
func TestPlanCardIsKeptWhereItWasDrawn(t *testing.T) {
	turn := &chatTurn{sse: &sseWriter{live: &syncBuf{}}, session: &ChatSession{ID: "s1"}, agent: AgentRecord{ID: "builder"}}
	presentPlan(t, turn, "one", "two")
	first := turn.session.BuildPlan.ID
	kept := turn.session.keptPlanCard(first)
	if kept == nil || kept.At.IsZero() || len(kept.Plan) != 2 {
		t.Fatalf("plan card not kept with a time: %+v", kept)
	}
	drawn := kept.At

	markDone(t, turn, 1)
	markDone(t, turn, 2)
	kept = turn.session.keptPlanCard(first)
	if kept == nil || !kept.At.Equal(drawn) {
		t.Fatalf("an update moved the card's time: %+v", kept)
	}
	if planStepsOpen(kept.Plan) {
		t.Fatalf("kept card does not show the finished steps: %+v", kept.Plan)
	}

	// The next plan gets a card of its own; the finished one stays as it was.
	presentPlan(t, turn, "three")
	if turn.session.BuildPlan.ID == first {
		t.Fatalf("a plan after a finished one rewrote its card")
	}
	if old := turn.session.keptPlanCard(first); old == nil || len(old.Plan) != 2 {
		t.Fatalf("the finished plan's card was lost: %+v", old)
	}

	// Replaced midway, a plan keeps its card: it is the same piece of work.
	second := turn.session.BuildPlan.ID
	presentPlan(t, turn, "three", "four")
	if turn.session.BuildPlan.ID != second {
		t.Fatalf("a plan replaced midway got a new card")
	}
}

// A tracked plan re-paints at the head of a turn only while it has steps open.
// A finished one is left where it is.
func TestFinishedWorkPlanIsNotRestored(t *testing.T) {
	plan := &WorkPlan{}
	if err := plan.SetSteps([]string{"a"}, []string{""}); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	turn := &chatTurn{sse: &sseWriter{live: buf}, session: &ChatSession{ID: "s2", WorkPlan: plan}, agent: AgentRecord{WorkPlan: true}}
	turn.restoreWorkPlanCard()
	if !strings.Contains(buf.String(), "orchestrate_plan") {
		t.Fatalf("an open plan was not restored")
	}
	plan.SetStatus(1, WorkStepDone)
	buf2 := &syncBuf{}
	turn.sse = &sseWriter{live: buf2}
	turn.restoreWorkPlanCard()
	if buf2.String() != "" {
		t.Fatalf("a finished plan was painted again: %s", buf2.String())
	}

	// A new plan after the finished one is drawn on a new card.
	before := turn.session.WorkPlanCard
	turn.workPlanChanged(WorkPlanChange{PlanID: "workplan:s2", Kind: "step", Steps: plan.Snapshot()})
	turn.workPlanChanged(WorkPlanChange{PlanID: "workplan:s2", Kind: "set", Steps: []WorkPlanStep{{ID: 1, Title: "b", Status: WorkStepPending}}})
	if turn.session.WorkPlanCard == before {
		t.Fatalf("a plan after a finished one reused its card %q", before)
	}
}
