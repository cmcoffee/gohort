package core

// The one advice rule that transfers between the two definitions with
// declared output fields. Machines have warned about it since they
// existed; pipelines have the identical mechanism and warned about
// nothing.

import (
	"strings"
	"testing"
)

func TestAPipelineStageIsToldWhenItHandRollsItsOwnJSON(t *testing.T) {
	d := PipelineDef{Name: "research", Stages: []PipelineStage{
		{Name: "plan", Kind: StageWorker, Prompt: "Break it up. Respond only with valid JSON.",
			Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "what to search"}}},
		// Declares nothing: the instruction is the author's own, about a
		// subject that happens to be JSON.
		{Name: "explain", Kind: StageWorker, Prompt: "explain this config as json"},
		// Declares fields and says nothing about format — the shape the
		// spec asks for.
		{Name: "judge", Kind: StageWorker, Prompt: "is it settled?",
			Output: []PipelineField{{Name: "done", Type: FieldBool, Desc: "settled"}}},
	}}
	adv := d.Advice()
	if len(adv) != 1 {
		t.Fatalf("expected exactly the hand-rolled one:\n%s", strings.Join(adv, "\n"))
	}
	if !strings.Contains(adv[0], "stage plan") || !strings.Contains(adv[0], "this stage already declares") {
		t.Errorf("the finding should read in the pipeline's own vocabulary: %s", adv[0])
	}

	// A stage whose only declarations are FILLED from variables has no
	// contract for the prompt to collide with — the model is never asked
	// for those.
	filled := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "pin", Kind: StageWorker, Prompt: "reply as json",
			Output: []PipelineField{{Name: "asked", Type: FieldString, From: "{input}"}}}}}
	if adv := filled.Advice(); len(adv) != 0 {
		t.Errorf("nothing is asked of the model here:\n%s", strings.Join(adv, "\n"))
	}

	// A loop body is written the same way and fails the same way, so it
	// is walked — and named so the reader can find it.
	nested := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "round", Kind: StageLoop, Count: 3, Body: []PipelineStage{
			{Name: "critique", Kind: StageWorker, Prompt: "judge it, return a JSON object",
				Output: []PipelineField{{Name: "verdict", Type: FieldString, Desc: "v"}}},
		}},
	}}
	adv = nested.Advice()
	if len(adv) != 1 || !strings.Contains(adv[0], "round › critique") {
		t.Fatalf("a stage inside a loop should be found and located:\n%s", strings.Join(adv, "\n"))
	}
}

// One sentence, two vocabularies. The machine editor matches its
// findings BY VALUE to decide which line carries the rewrite button, so
// the shared helper must keep producing exactly what it produced before.
func TestTheSharedFindingKeepsBothWordings(t *testing.T) {
	step := DeclaredOutputPromptAdvice("step", "decompose")
	if !strings.HasPrefix(step, "step decompose: the prompt asks for JSON, but this step already declares fields") {
		t.Errorf("the machine wording moved, which would strand the rewrite button: %s", step)
	}
	stage := DeclaredOutputPromptAdvice("stage", "plan")
	if !strings.Contains(stage, "this stage already declares") {
		t.Errorf("the pipeline wording should say stage: %s", stage)
	}
	// Still the same advice, not two different ones.
	if strings.TrimPrefix(step, "step decompose") != strings.ReplaceAll(
		strings.TrimPrefix(stage, "stage plan"), "stage", "step") {
		t.Error("the two readings have drifted apart")
	}
}

// The starter has to RUN. A "new" that opens on something the server
// would refuse teaches the wrong lesson about the whole feature in the
// first ten seconds — and a starter carrying a finding teaches the
// finding.
func TestTheStarterPipelineRunsAsWritten(t *testing.T) {
	s := StarterPipeline()
	if err := s.Validate(); err != nil {
		t.Fatalf("the starter does not validate:\n%v", err)
	}
	if adv := s.Advice(); len(adv) != 0 {
		t.Errorf("the starter carries findings its reader would see:\n%s", strings.Join(adv, "\n"))
	}
	// It teaches the thing that is easiest to get wrong: a later stage
	// reads an earlier one BY NAME.
	if !strings.Contains(s.Stages[1].Prompt, "{stage:plan.focus}") {
		t.Error("the starter should demonstrate a stage reading another")
	}
	// And it needs nothing this deployment might not have.
	for _, st := range s.Stages {
		if len(st.Tools) > 0 || strings.TrimSpace(st.Agent) != "" {
			t.Errorf("the starter must run anywhere: %+v", st)
		}
	}
	if len(s.Graph().Nodes) != 2 {
		t.Error("and it should draw")
	}
}
