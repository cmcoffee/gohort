package orchestrate

import (
	"context"
	"strings"
	"testing"
)

// TestBuildPlanStepsAreTolerant guards the fix for the plan-step tooling that
// dominated the CalDAV session: mark_step_* hard-errored on a stale/out-of-range
// step number ("step N exceeds plan length M") and on a second in_progress
// ("already in_progress"), which drove the 27B into turn-wasting apology loops.
// These are cosmetic checklist updates — they must soft-note and let the model
// keep working, and mark_step_in_progress must auto-advance instead of refusing.
func TestBuildPlanStepsAreTolerant(t *testing.T) {
	newTurn := func() *chatTurn {
		return &chatTurn{session: &ChatSession{BuildWorkPlan: buildPlanWith([]string{"pending", "pending"}, "one", "two")}}
	}

	// Out-of-range mark_step_done: no error, soft note, work continues.
	turn := newTurn()
	out, err := turn.markStepDoneToolDef().Handler(context.Background(), map[string]any{"step": 20, "summary": "x"})
	if err != nil {
		t.Fatalf("out-of-range mark_step_done must not error; got %v", err)
	}
	if !strings.Contains(out, "no step 20") {
		t.Fatalf("expected soft note about missing step; got %q", out)
	}

	// mark_step_in_progress auto-advances a stale in_progress step (no refusal).
	turn = newTurn()
	if _, err := turn.markStepInProgressToolDef().Handler(context.Background(), map[string]any{"step": 1}); err != nil {
		t.Fatalf("first in_progress: %v", err)
	}
	out, err = turn.markStepInProgressToolDef().Handler(context.Background(), map[string]any{"step": 2})
	if err != nil {
		t.Fatalf("second in_progress must not refuse; got %v", err)
	}
	if !strings.Contains(out, "auto-completed") {
		t.Fatalf("expected auto-advance note; got %q", out)
	}
	steps := turn.session.BuildWorkPlan.Snapshot()
	if steps[0].Status != "done" || steps[1].Status != "in_progress" {
		t.Fatalf("stale step should be done, new one in_progress; got %q / %q", steps[0].Status, steps[1].Status)
	}

	// No plan at all: soft note, not an error.
	empty := &chatTurn{session: &ChatSession{}}
	if out, err := empty.markStepDoneToolDef().Handler(context.Background(), map[string]any{"step": 1, "summary": "x"}); err != nil || !strings.Contains(out, "No active build plan") {
		t.Fatalf("no-plan mark_step_done should soft-note; got %q, %v", out, err)
	}
}

// A step is found by its number wherever a revision has moved it. The step
// tools used to count positions, so after a reorder mark_step_done(step=1)
// marked whichever step had landed first.
func TestBuildPlanStepsGoByNumberAfterAReorder(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{ID: "s1"}}
	presentPlan(t, turn, "one", "two", "three")
	if _, err := turn.reviseBuildPlanToolDef().Handler(context.Background(), map[string]any{"action": "reorder", "order": []any{float64(3), float64(1), float64(2)}}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	markDone(t, turn, 1)
	for _, s := range turn.session.BuildWorkPlan.Snapshot() {
		if want := s.ID == 1; (s.Status == "done") != want {
			t.Errorf("step %d (%s) is %s after marking step 1 done", s.ID, s.Title, s.Status)
		}
	}
}
