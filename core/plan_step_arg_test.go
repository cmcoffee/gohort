package core

import (
	"strings"
	"testing"
)

// The parameter is step_id and models routinely write "step". Reported live:
// an agent called mark_step_in_progress({"step": 1}) four rounds running,
// narrating "let me start" each time, never reaching the tool it was asked to
// test — because a missing integer reads as 0 and "step 0 not found in plan"
// does not tell anyone which WORD to change.
func TestStepIDAcceptsTheNamesModelsWrite(t *testing.T) {
	for _, args := range []map[string]any{
		{"step_id": 1},
		{"step": 1}, // the reported case
		{"id": 1},
		{"stepId": 1},
		{"step_id": "1"}, // stringified, as some providers send
		{"step_id": float64(1)},
	} {
		got, ok := stepIDArg(args)
		if !ok || got != 1 {
			t.Errorf("%v resolved to (%d, %v), want (1, true)", args, got, ok)
		}
	}
	// Nothing usable must REPORT nothing usable rather than resolving to 0 and
	// producing an error about a step that was never named.
	for _, args := range []map[string]any{
		{},
		{"reason": "because"},
		{"step_id": nil},
	} {
		if _, ok := stepIDArg(args); ok {
			t.Errorf("%v claimed to carry a step id", args)
		}
	}
}

// Re-marking the step that is already in progress must SAY it changed nothing.
// A cheerful success reads as progress, and an agent just told it progressed
// will happily say so again — which is exactly the four identical rounds.
func TestReMarkingTheActiveStepReportsNoChange(t *testing.T) {
	var plan WorkPlan
	if err := plan.SetSteps([]string{"list submolts", "fetch feed"}, []string{"which submolts exist", "what is on the feed"}); err != nil {
		t.Fatal(err)
	}
	tools := WorkPlanTools(WorkPlanToolSpec{Plan: &plan})

	var start AgentToolDef
	for _, td := range tools.All() {
		if td.Tool.Name == "mark_step_in_progress" {
			start = td
		}
	}
	if start.Handler == nil {
		t.Fatal("no mark_step_in_progress tool")
	}

	first, err := start.Handler(map[string]any{"step_id": 1})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if strings.Contains(first, "ALREADY") {
		t.Errorf("the first call reported no change: %s", first)
	}

	// The same call again — the shape that looped.
	second, err := start.Handler(map[string]any{"step": 1})
	if err != nil {
		t.Fatalf("the synonym form failed: %v", err)
	}
	if !strings.Contains(second, "ALREADY") {
		t.Fatalf("re-marking the active step reported success again: %s", second)
	}
	// And it must say what to do INSTEAD, or the model has been told to stop
	// without being told to start.
	if !strings.Contains(second, "record_step_findings") {
		t.Errorf("the no-op message does not say what to do next: %s", second)
	}
}

// A missing step id names the parameter to use, rather than failing about a
// step nobody mentioned.
func TestAMissingStepIDNamesTheParameter(t *testing.T) {
	var plan WorkPlan
	_ = plan.SetSteps([]string{"one"}, []string{"anything"})
	tools := WorkPlanTools(WorkPlanToolSpec{Plan: &plan})
	for _, td := range tools.All() {
		if td.Tool.Name != "mark_step_in_progress" {
			continue
		}
		_, err := td.Handler(map[string]any{})
		if err == nil {
			t.Fatal("a call with no step id succeeded")
		}
		if !strings.Contains(err.Error(), "step_id") {
			t.Errorf("the error does not name the parameter: %v", err)
		}
	}
}
