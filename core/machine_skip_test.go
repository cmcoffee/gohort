package core

import (
	"context"
	"strings"
	"testing"
)

// A step that reports it does not apply is recorded as skipped, passes what
// it was handed through as its result, leaves a note saying why, and the run
// moves on to the step's Next. When the skipped step is the last, the run's
// result is what the step before it produced.
func TestASkippedStepPassesThroughAndMovesOn(t *testing.T) {
	def := MachineDef{Name: "m", Start: "a", Unattended: true, Phases: []MachinePhase{
		{Name: "a", Prompt: "find", Next: "b"},
		{Name: "b", Prompt: "enrich", Next: "c", Tools: []string{"lookup"}},
		{Name: "c", Prompt: "polish", Tools: []string{"lookup"}},
	}}
	var ran []string
	run := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		ran = append(ran, ph.Name)
		switch ph.Name {
		case "a":
			return "the finding", nil
		default:
			if !SkipStep(ctx, "nothing to "+ph.Name) {
				t.Fatalf("SkipStep found no step to end inside step %s", ph.Name)
			}
			return "", nil
		}
	}
	var notes []string
	cur := &MachineCursor{}
	final, text, err := new(AppCore).RunUnattended(context.Background(), def, cur, MachineTurn{Input: "go"}, run,
		func(kind, detail string) { notes = append(notes, kind+": "+detail) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, ",") != "a,b,c" {
		t.Fatalf("ran %v; a skip moves on to the step's next", ran)
	}
	if final.Name != "c" || text != "the finding" {
		t.Fatalf("finished at %s with %q; want c, passing a's result through", final.Name, text)
	}
	b := cur.State["b"]
	if !b.Skipped || b.SkipReason != "nothing to b" || b.Text != "the finding" || b.Fields != nil {
		t.Fatalf("b's result = %+v", b)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "machine_step_skipped: step b did not apply: nothing to b; moving on to c") {
		t.Fatalf("no note for the skip:\n%s", strings.Join(notes, "\n"))
	}
}

// A skip goes to Next even on a step that routes: a step that did not apply
// decided nothing, so there is no choice of destination to read.
func TestASkippedRouterGoesToNext(t *testing.T) {
	def := MachineDef{Name: "m", Start: "route", Unattended: true, Phases: []MachinePhase{
		{Name: "route", Prompt: "pick", Choices: []string{"left", "right"}, Next: "right", Tools: []string{"x"}},
		{Name: "left", Prompt: "l"},
		{Name: "right", Prompt: "r"},
	}}
	calls := 0
	run := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		calls++
		if ph.Name == "route" {
			SkipStep(ctx, "no message to route")
			return "", nil
		}
		return ph.Name + " ran", nil
	}
	final, text, err := new(AppCore).RunUnattended(context.Background(), def, &MachineCursor{}, MachineTurn{Input: "go"}, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if final.Name != "right" || text != "right ran" {
		t.Fatalf("finished at %s (%q); a skipped router goes to its next", final.Name, text)
	}
	// One call for the router, one for right: a skip is not followed by the
	// repair call a declared-output step makes when its reply does not decode.
	if calls != 2 {
		t.Fatalf("%d runner calls, want 2", calls)
	}
}

// The way out is offered to a step that runs its own model with tools, and to
// no other: a required step, a step that reaches nothing, a step another
// runner does, a resident step.
func TestWhichStepsAreOfferedTheWayOut(t *testing.T) {
	for _, c := range []struct {
		name string
		ph   MachinePhase
		want bool
	}{
		{"reaches tools", MachinePhase{Name: "s", Prompt: "p", Tools: []string{"x"}}, true},
		{"reach all", MachinePhase{Name: "s", Prompt: "p", Reach: ReachAll}, true},
		{"required", MachinePhase{Name: "s", Prompt: "p", Tools: []string{"x"}, Required: true}, false},
		{"reaches nothing", MachinePhase{Name: "s", Prompt: "p"}, false},
		{"a tool runs it", MachinePhase{Name: "s", Tool: "x"}, false},
		{"an agent runs it", MachinePhase{Name: "s", Agent: "helper", Tools: []string{"x"}}, false},
		{"resident", MachinePhase{Name: "s", Prompt: "p", Resident: true, Tools: []string{"x"}}, false},
	} {
		if got := offersSkip(c.ph); got != c.want {
			t.Errorf("%s: offersSkip = %v, want %v", c.name, got, c.want)
		}
	}
}

// The skip tool ends the step through the slot, and refuses outside one.
func TestTheSkipToolEndsTheStep(t *testing.T) {
	tool := skipStepTool()
	ctx, read := withSkipSlot(context.Background())
	if _, err := tool.Handler(ctx, map[string]any{"reason": "no order to look up"}); err != nil {
		t.Fatal(err)
	}
	if reason, ok := read(); !ok || reason != "no order to look up" {
		t.Fatalf("slot = %q %v", reason, ok)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"reason": "x"}); err == nil {
		t.Fatal("skip_step outside a machine step succeeded")
	}
	if SkipStep(context.Background(), "x") {
		t.Fatal("SkipStep reported a step to end where there is none")
	}
}

// A runner can read the step's real variables: the run's input and what the
// step before it produced. A tool step's arguments are templated from these.
func TestStepVarsCarryTheRunsInputAndPrev(t *testing.T) {
	def := MachineDef{Name: "m", Start: "a", Unattended: true, Phases: []MachinePhase{
		{Name: "a", Prompt: "first", Next: "b"},
		{Name: "b", Tool: "lookup", Args: map[string]string{"q": "{input}"}},
	}}
	var got PhaseVars
	run := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		if ph.Name == "a" {
			return "from a", nil
		}
		v, ok := StepVars(ctx)
		if !ok {
			t.Fatal("no step vars inside a step")
		}
		got = v
		return "done", nil
	}
	if _, _, err := new(AppCore).RunUnattended(context.Background(), def, &MachineCursor{}, MachineTurn{Input: "Reykjavik"}, run, nil); err != nil {
		t.Fatal(err)
	}
	if got.Input != "Reykjavik" || got.Prev != "from a" || got.Step != "b" || got.Machine != "m" {
		t.Fatalf("step vars = input %q prev %q step %q machine %q", got.Input, got.Prev, got.Step, got.Machine)
	}
	if ResolvePhaseTemplate("{input}", got, nil) != "Reykjavik" {
		t.Fatal("{input} does not resolve from the step's vars")
	}
	if _, ok := StepVars(context.Background()); ok {
		t.Fatal("step vars outside a step")
	}
}
