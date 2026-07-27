package core

// The branch stage: read a bool an earlier stage declared, then either
// end the pipeline or skip forward past stages that no longer apply.
// The two shapes it exists for are "input rejected, stop" and "this was
// supplied already, skip the stage that derives it".

import (
	"context"
	"strings"
	"testing"
)

func branchDef(skipTo string) PipelineDef {
	return PipelineDef{Stages: []PipelineStage{
		{Name: "frame", Kind: StageAgent, Agent: "framer", Prompt: "frame {input}",
			Output: []PipelineField{{Name: "rejected", Type: FieldBool, Required: true}}},
		{Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: skipTo},
		{Name: "work", Kind: StageAgent, Agent: "worker", Prompt: "do the work"},
		{Name: "report", Kind: StageAgent, Agent: "reporter", Prompt: "report on {prev}"},
	}}
}

// --- validation -------------------------------------------------------

func TestValidate_BranchShape(t *testing.T) {
	if err := branchDef("").Validate(); err != nil {
		t.Fatalf("stop-form branch rejected: %v", err)
	}
	if err := branchDef("report").Validate(); err != nil {
		t.Fatalf("skip-form branch rejected: %v", err)
	}

	bool1 := PipelineStage{Name: "frame", Prompt: "x",
		Output: []PipelineField{{Name: "rejected", Type: FieldBool}}}
	bad := map[string]PipelineDef{
		"no when": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch},
		}},
		"when names a stage, not a field": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame"},
		}},
		"when is not bool": {Stages: []PipelineStage{
			{Name: "frame", Prompt: "x", Output: []PipelineField{{Name: "note", Type: FieldString}}},
			{Name: "gate", Kind: StageBranch, When: "frame.note"},
		}},
		"when references a later stage": {Stages: []PipelineStage{
			{Name: "gate", Kind: StageBranch, When: "frame.rejected"}, bool1,
		}},
		"skip_to is unknown": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "nowhere"},
		}},
		// Backward jumps are iteration, and iteration is kind=loop where
		// count bounds it — allowing one here would be unbounded looping
		// through the back door.
		"skip_to points backwards": {Stages: []PipelineStage{
			bool1,
			{Name: "earlier", Prompt: "x"},
			{Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "earlier"},
		}},
		"skip_to points at itself": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected", SkipTo: "gate"},
		}},
		"branch declares output": {Stages: []PipelineStage{
			bool1, {Name: "gate", Kind: StageBranch, When: "frame.rejected",
				Output: []PipelineField{{Name: "q"}}},
		}},
		"when on a non-branch stage": {Stages: []PipelineStage{
			bool1, {Name: "other", Prompt: "x", When: "frame.rejected"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func TestValidate_BranchInLoopBody(t *testing.T) {
	body := []PipelineStage{
		{Name: "check", Prompt: "x", Output: []PipelineField{{Name: "bad", Type: FieldBool}}},
		{Name: "gate", Kind: StageBranch, When: "check.bad", SkipTo: "tail"},
		{Name: "mid", Prompt: "x"},
		{Name: "tail", Prompt: "x"},
	}
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: body},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("skip-within-body should be allowed: %v", err)
	}
	// Ending the PIPELINE from inside a pass is ambiguous — the loop
	// already has until for stopping early.
	stopInBody := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "check", Prompt: "x", Output: []PipelineField{{Name: "bad", Type: FieldBool}}},
			{Name: "gate", Kind: StageBranch, When: "check.bad"},
		}},
	}}
	err := stopInBody.Validate()
	if err == nil {
		t.Fatal("a pipeline-ending branch inside a loop body should be rejected")
	}
	if !strings.Contains(err.Error(), "until") {
		t.Errorf("the error should point at the loop's until: %v", err)
	}
}

// --- execution --------------------------------------------------------

func TestBranch_StopsThePipeline(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": true}`
		}
		return "should not run"
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef(""), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 1 {
		t.Errorf("only the framing stage should have run, got %d calls: %v", len(rec.prompts), rec.prompts)
	}
	// Stopping returns whatever the last stage produced, so the rejection
	// itself is the pipeline's answer.
	if !strings.Contains(out, "rejected") {
		t.Errorf("stop should return the last stage's output, got %q", out)
	}
}

func TestBranch_FallsThroughWhenFalse(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": false}`
		}
		return "did " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef(""), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(rec.prompts) != 3 {
		t.Errorf("all three stages should have run, got %d", len(rec.prompts))
	}
	if out != "did reporter" {
		t.Errorf("output = %q", out)
	}
}

func TestBranch_SkipsForward(t *testing.T) {
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "framer" {
			return `{"rejected": true}`
		}
		return "did " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), branchDef("report"), "topic", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	// framer + reporter — "work" is jumped over.
	if len(rec.prompts) != 2 {
		t.Fatalf("expected framer + reporter, got %d: %v", len(rec.prompts), rec.prompts)
	}
	if !strings.Contains(rec.prompts[1], "report on") {
		t.Errorf("second call should be the reporter: %q", rec.prompts[1])
	}
	if out != "did reporter" {
		t.Errorf("output = %q", out)
	}
}

func TestBranch_MissingSourceReadsAsFalse(t *testing.T) {
	// A chain of branches where the first skips past the stage the second
	// reads. Falling through (running the work) is the safe direction;
	// treating an absent value as true would skip real work.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "a", Kind: StageAgent, Agent: "x", Prompt: "p",
			Output: []PipelineField{{Name: "skip", Type: FieldBool}}},
		{Name: "g1", Kind: StageBranch, When: "a.skip", SkipTo: "g2"},
		{Name: "b", Kind: StageAgent, Agent: "y", Prompt: "p",
			Output: []PipelineField{{Name: "stop", Type: FieldBool}}},
		{Name: "g2", Kind: StageBranch, When: "b.stop"},
		{Name: "tail", Kind: StageAgent, Agent: "z", Prompt: "p"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	rec := &recorder{reply: func(agent, _ string, _ int) string {
		if agent == "x" {
			return `{"skip": true}`
		}
		return "ran " + agent
	}}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "in", rec.fn, nil, nil)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if out != "ran z" {
		t.Errorf("g2 should have read false and fallen through, got %q", out)
	}
}
