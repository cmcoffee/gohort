package core

// The loop stage: repeat a body, thread each pass into the next, stop on
// count or on an Until field going true.
//
// Where fanout does breadth (N independent branches in parallel), loop
// does depth — the carry between passes is the whole point, so these
// tests assert the carry as much as the iteration count.

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// --- validation -------------------------------------------------------

func TestValidate_LoopShape(t *testing.T) {
	body := []PipelineStage{{Name: "step", Prompt: "do {iteration}"}}
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 3, Body: body},
		{Name: "wrap", Prompt: "summarize {stage:rounds}"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid loop rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"no body": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2},
		}},
		"no count": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Body: body},
		}},
		"count over the ceiling": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: loopMaxIterations + 1, Body: body},
		}},
		"body on a non-loop": {Stages: []PipelineStage{
			{Name: "plain", Body: body},
		}},
		"output on a loop": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body,
				Output: []PipelineField{{Name: "q"}}},
		}},
		"bad collect": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body, Collect: "some"},
		}},
		"loops do not nest": {Stages: []PipelineStage{
			{Name: "outer", Kind: StageLoop, Count: 2, Body: []PipelineStage{
				{Name: "inner", Kind: StageLoop, Count: 2, Body: []PipelineStage{{Name: "deep", Prompt: "x"}}},
			}},
		}},
		// A body name is per-pass, so a reference from outside would mean
		// "whatever the last pass left" — caught at save time instead.
		"outer stage references a body stage": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Body: body},
			{Name: "after", Prompt: "look at {stage:step}"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_LoopUntil(t *testing.T) {
	checker := PipelineStage{Name: "check", Prompt: "done?",
		Output: []PipelineField{{Name: "done", Type: FieldBool}}}

	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check.done",
			Body: []PipelineStage{{Name: "work", Prompt: "x"}, checker}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid until rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"until names a stage, not a field": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check",
				Body: []PipelineStage{checker}},
		}},
		"until field is not bool": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "check.note",
				Body: []PipelineStage{{Name: "check", Prompt: "x",
					Output: []PipelineField{{Name: "note", Type: FieldString}}}}},
		}},
		"until field is not declared": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "work.done",
				Body: []PipelineStage{{Name: "work", Prompt: "x"}}},
		}},
		// An outer field can't change between passes, so the loop would
		// run once or all N times — never what the author meant.
		"until points outside the loop": {Stages: []PipelineStage{
			{Name: "gate", Prompt: "x", Output: []PipelineField{{Name: "done", Type: FieldBool}}},
			{Name: "rounds", Kind: StageLoop, Count: 5, Until: "gate.done",
				Body: []PipelineStage{{Name: "work", Prompt: "x"}}},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// --- execution --------------------------------------------------------

// recorder is a dispatch hook that echoes a scripted reply per agent and
// records the prompt it was handed, so a test can assert the carry.
type recorder struct {
	mu      sync.Mutex
	prompts []string
	reply   func(agent, prompt string, n int) string
	n       int
}

func (rec *recorder) fn(_ context.Context, agent, prompt string) (string, error) {
	rec.mu.Lock()
	rec.n++
	n := rec.n
	rec.prompts = append(rec.prompts, prompt)
	rec.mu.Unlock()
	return rec.reply(agent, prompt, n), nil
}

func TestLoop_CarriesEachPassIntoTheNext(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 3, Body: []PipelineStage{
			{Name: "step", Kind: StageAgent, Agent: "w",
				Prompt: "pass {iteration} of {iterations}, previous was: {prev}"},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	rec := &recorder{reply: func(_, _ string, n int) string { return "result-" + strconv.Itoa(n) }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Fatalf("expected 3 passes, got %d", len(rec.prompts))
	}
	// {iteration} substitutes per pass...
	if !strings.Contains(rec.prompts[0], "pass 1 of 3") || !strings.Contains(rec.prompts[2], "pass 3 of 3") {
		t.Errorf("{iteration}/{iterations} not substituted: %v", rec.prompts)
	}
	// ...and each pass sees the PREVIOUS pass's output, which is the
	// difference between a loop and a fanout.
	if !strings.Contains(rec.prompts[0], "previous was: seed") {
		t.Errorf("pass 1 should carry the pipeline input: %q", rec.prompts[0])
	}
	if !strings.Contains(rec.prompts[1], "previous was: result-1") {
		t.Errorf("pass 2 should carry pass 1's output: %q", rec.prompts[1])
	}
	if !strings.Contains(rec.prompts[2], "previous was: result-2") {
		t.Errorf("pass 3 should carry pass 2's output: %q", rec.prompts[2])
	}
	if out != "result-3" {
		t.Errorf("collect defaults to last, got %q", out)
	}
}

func TestLoop_CollectAll(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Collect: "all", Body: []PipelineStage{
			{Name: "step", Kind: StageAgent, Agent: "w", Prompt: "x"},
		}},
	}}
	rec := &recorder{reply: func(_, _ string, n int) string { return "said-" + strconv.Itoa(n) }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	for _, want := range []string{"## Pass 1", "said-1", "## Pass 2", "said-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("collect=all output missing %q:\n%s", want, out)
		}
	}
}

func TestLoop_UntilStopsEarly(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 10, Until: "check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageAgent, Agent: "w", Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool, Required: true}}},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	// False, false, then true — should stop after the third pass, not run
	// all ten.
	rec := &recorder{reply: func(_, _ string, n int) string {
		if n >= 3 {
			return `{"done": true}`
		}
		return `{"done": false}`
	}}
	app := &AppCore{}
	if _, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Errorf("until should have stopped the loop after 3 passes, got %d", len(rec.prompts))
	}
}

func TestLoop_UntilNeverTrueStillTerminates(t *testing.T) {
	// The ceiling is the guarantee — a model that never says "done" must
	// not be able to run a pipeline forever.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 4, Until: "check.done", Body: []PipelineStage{
			{Name: "check", Kind: StageAgent, Agent: "w", Prompt: "done?",
				Output: []PipelineField{{Name: "done", Type: FieldBool}}},
		}},
	}}
	rec := &recorder{reply: func(_, _ string, _ int) string { return `{"done": false}` }}
	app := &AppCore{}
	if _, err := app.executePipelineDef(context.Background(), def, "seed", rec.fn, nil, nil); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 4 {
		t.Errorf("expected exactly count passes, got %d", len(rec.prompts))
	}
}
