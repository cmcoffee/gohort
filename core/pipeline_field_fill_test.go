package core

// A stage field taken from a variable rather than asked for. The shared
// PipelineField grew From for machines; the pipeline tool learned to
// decode it before the pipeline runtime honoured it, which is a
// documented feature that does nothing.

import (
	"context"
	"strings"
	"testing"
)

func TestAStageFieldCanBeFilledRatherThanAsked(t *testing.T) {
	stage := PipelineStage{Name: "triage", Kind: StageAgent, Agent: "w", Prompt: "sort it",
		Output: []PipelineField{
			{Name: "asked", Type: FieldString, From: "{input}"},
			{Name: "lane", Type: FieldString, Desc: "which lane"},
		}}

	// The model is asked for exactly what it must answer.
	model := stage.ModelOutput()
	if len(model) != 1 || model[0].Name != "lane" {
		t.Fatalf("a filled field must not be asked for: %+v", model)
	}
	if static := stage.StaticFields(); len(static) != 1 || static[0].Name != "asked" {
		t.Fatalf("and it must still be part of what the stage establishes: %+v", static)
	}

	// End to end: the filled field lands in the stage's result, where
	// {stage:triage.asked} reads it like any other.
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		stage,
		{Name: "reply", Kind: StageAgent, Agent: "w",
			Prompt: "the question was {stage:triage.asked}, lane {stage:triage.lane}"},
	}}
	rec := &recorder{reply: func(_, _ string, n int) string {
		if n == 1 {
			return `{"lane":"deep"}`
		}
		return "done"
	}}
	if _, err := (&AppCore{}).RunPipelineDefSync(context.Background(), def,
		"why is the export failing?", rec.fn, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rec.prompts) != 2 {
		t.Fatalf("expected two stages to run, got %d", len(rec.prompts))
	}
	if !strings.Contains(rec.prompts[1], "the question was why is the export failing?") {
		t.Errorf("the filled field should reach a later stage:\n%s", rec.prompts[1])
	}
	if !strings.Contains(rec.prompts[1], "lane deep") {
		t.Errorf("alongside the ones the model answered:\n%s", rec.prompts[1])
	}
	// And the model was never shown the field it did not have to answer.
	if strings.Contains(rec.prompts[0], `"asked"`) {
		t.Errorf("a filled field must not appear in the contract:\n%s", rec.prompts[0])
	}
}
