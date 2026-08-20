// What a pipeline will do, before it does it.
package core

import (
	"strings"
	"testing"
)

// The arithmetic nobody does in their head while authoring: a loop of two
// stages, three times, with a fanout inside it, is not "three stages".
func TestThePlanMultipliesWhatNests(t *testing.T) {
	def := PipelineDef{Name: "research", Stages: []PipelineStage{
		{Name: "plan", Kind: StageWorker, Prompt: "split {input}"},
		{Name: "each", Kind: StageLoop, Count: 3, Prompt: "go", Body: []PipelineStage{
			{Name: "dig", Kind: StageWorker, Prompt: "look at {prev}"},
			{Name: "check", Kind: StageWorker, Prompt: "verify {prev}"},
		}},
		{Name: "write", Kind: StageSynthesize, Prompt: "write up {stage:each}"},
	}}
	plan := def.Plan()

	// plan(1) + loop(2 per pass, up to 3 passes = 6) + write(1)
	if plan.Min != 4 || plan.Max != 8 {
		t.Errorf("cost should be 4–8 calls, got %d–%d", plan.Min, plan.Max)
	}
	// Body stages are LISTED so the shape is readable, but not re-counted —
	// the loop's own figures already multiplied them.
	var bodySeen bool
	for _, s := range plan.Steps {
		if s.Name == "dig" {
			bodySeen = true
			if s.Depth != 1 || s.Min != 0 || s.Max != 0 {
				t.Errorf("a body stage should be shown nested and counted by its parent: %+v", s)
			}
		}
	}
	if !bodySeen {
		t.Error("the body stages should appear in the walk")
	}
	if !strings.Contains(plan.Summary(), "4–8 model calls") {
		t.Errorf("the headline should carry the range: %q", plan.Summary())
	}
}

// The sharp one: nothing auto-supplies a stage with what came before it. A
// prompt placing no placeholder sees its own text and nothing else, and the
// symptom at run time is a fluent answer to the wrong question.
func TestAStageThatReadsNothingSaysSo(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "search for {input}"},
		{Name: "summarize", Kind: StageWorker, Prompt: "write a summary."},
	}}
	steps := def.Plan().Steps
	if len(steps[0].Reads) == 0 {
		t.Error("a stage placing {input} reads the run's input")
	}
	if len(steps[1].Reads) != 0 {
		t.Errorf("a prompt with no placeholder reads nothing: %v", steps[1].Reads)
	}

	// And every shape of reference is recognised, including a field.
	refs := PipelineStage{Kind: StageWorker,
		Prompt: "compare {stage:plan.queries} with {stage:dig} for {item}"}
	got := strings.Join(stageReads(refs), " | ")
	for _, want := range []string{"stage plan.queries", "stage dig", "the current item"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
}

// A panel's cost is voices times rounds, and a fanout's is bounded by the cap
// rather than unknowable — the cap is the number somebody sizing a bill needs.
func TestPanelAndFanoutCosts(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "debate", Kind: StagePanel, Count: 2, Panel: []string{"A", "B", "C"}, Prompt: "argue"},
		{Name: "each", Kind: StageFanout, FanOver: "debate", Prompt: "dig {item}"},
	}}
	steps := def.Plan().Steps
	if steps[0].Min != 6 || steps[0].Max != 6 {
		t.Errorf("3 voices x 2 rounds is 6 calls, got %d–%d", steps[0].Min, steps[0].Max)
	}
	if steps[1].Min != 1 || steps[1].Max != FanoutMaxItems {
		t.Errorf("a fanout runs once per item up to the cap, got %d–%d", steps[1].Min, steps[1].Max)
	}
	if !strings.Contains(steps[1].Note, "not known until") {
		t.Errorf("it should say the count is not knowable yet: %q", steps[1].Note)
	}
}

// A stage that spends no tokens should say so rather than being counted.
func TestFreeStagesCostNothing(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "fetch", Kind: StageTool, Tool: "http_get", Args: map[string]string{"url": "x"}},
		{Name: "stop", Kind: StageBranch, When: "check.done"},
	}}
	plan := def.Plan()
	if plan.Max != 0 {
		t.Errorf("neither stage runs a model: %d", plan.Max)
	}
	if !strings.Contains(plan.Steps[0].RunBy, "no model") || !strings.Contains(plan.Steps[1].RunBy, "no model") {
		t.Errorf("both should say why they are free: %+v", plan.Steps)
	}
	if !strings.Contains(plan.Steps[1].Note, "ENDS the run") {
		t.Errorf("a branch with no skip_to ends the run, which is worth stating: %q", plan.Steps[1].Note)
	}
}
