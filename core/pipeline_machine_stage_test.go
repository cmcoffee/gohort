package core

// The kind=machine stage: a whole run as one stage.
//
// The counterpart of the machine's pipeline phase. Together they let the
// two primitives compose in both directions, and the shape that wants it
// is a fanout body whose branch is a child run.

import (
	"context"
	"strings"
	"testing"
)

func machineStagePipeline(out []PipelineField) PipelineDef {
	return PipelineDef{Stages: []PipelineStage{
		{Name: "fill", Kind: StageMachine, Machine: "Gap filler",
			Prompt: "fill the gap in {input}", Output: out},
	}}
}

func TestValidate_MachineStageShape(t *testing.T) {
	if err := machineStagePipeline(nil).Validate(); err != nil {
		t.Fatalf("a valid machine stage was rejected: %v", err)
	}
	bad := map[string]PipelineDef{
		"names no machine": {Stages: []PipelineStage{
			{Name: "fill", Kind: StageMachine, Prompt: "x"},
		}},
		"names an agent as well": {Stages: []PipelineStage{
			{Name: "fill", Kind: StageMachine, Machine: "Gap filler", Agent: "w", Prompt: "x"},
		}},
		"machine on another kind": {Stages: []PipelineStage{
			{Name: "plain", Prompt: "x", Machine: "Gap filler"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

// Without a host to run machines, the stage says so rather than failing
// somewhere less useful. Same posture as an agent stage with no dispatch.
func TestMachineStageWithoutARunnerSaysSo(t *testing.T) {
	_, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(nil), "the gap", PipelineHooks{})
	if err == nil {
		t.Fatal("a machine stage with no runner should refuse")
	}
	if !strings.Contains(err.Error(), "machine runner") {
		t.Errorf("the refusal should name what is missing: %v", err)
	}
}

func TestMachineStageRunsAndCarriesItsText(t *testing.T) {
	var gotRef, gotInput string
	out, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(nil), "the export failure", PipelineHooks{
			Machine: func(_ context.Context, ref, input string) (string, map[string]any, error) {
				gotRef, gotInput = ref, input
				return "the child run's report", nil, nil
			},
		})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if gotRef != "Gap filler" {
		t.Errorf("the stage should ask for the machine it names, got %q", gotRef)
	}
	if !strings.Contains(gotInput, "the export failure") {
		t.Errorf("the resolved prompt should reach the run: %q", gotInput)
	}
	if out != "the child run's report" {
		t.Errorf("the run's own result should be the stage's, got %q", out)
	}
}

// The one-call path: the child's last step already declared these fields,
// so nothing has to re-read them out of its prose.
func TestMachineStageTakesTheRunsOwnShape(t *testing.T) {
	declared := []PipelineField{{Name: "found", Type: FieldString}}
	_, fields, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return "prose the stage does not have to parse",
					map[string]any{"found": "a missing index", "extra": "not asked for"}, nil
			},
		})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if fields["found"] != "a missing index" {
		t.Errorf("the declared field should come straight across: %#v", fields)
	}
	if _, leaked := fields["extra"]; leaked {
		t.Error("a field the stage never declared should not land on it")
	}
}

// Shapes that do not line up fall back to decoding the text, and when
// that fails the message says which side to fix rather than re-running
// the machine to reshape its answer.
func TestMachineStageMismatchExplainsWhichSideToFix(t *testing.T) {
	declared := []PipelineField{{Name: "found", Type: FieldString}}
	// JSON text still satisfies the contract even when the run reported no
	// fields of its own.
	if _, fields, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return `{"found": "a missing index"}`, nil, nil
			},
		}); err != nil || fields["found"] != "a missing index" {
		t.Errorf("a JSON result should still decode: %v / %#v", err, fields)
	}
	_, _, err := new(AppCore).RunPipelineDefHooks(context.Background(),
		machineStagePipeline(declared), "x", PipelineHooks{
			Machine: func(context.Context, string, string) (string, map[string]any, error) {
				return "just prose", map[string]any{"something_else": 1}, nil
			},
		})
	if err == nil {
		t.Fatal("a result that carries neither the fields nor decodable JSON should fail")
	}
	if !strings.Contains(err.Error(), "LAST step") {
		t.Errorf("the failure should say where to declare them: %v", err)
	}
}

// The shape this whole stage kind exists for: a child run per item,
// several at once, instead of one after another.
func TestAFanoutBodyCanRunAChildRunPerItem(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "gaps", Kind: StageAgent, Agent: "finder", Prompt: "find gaps",
			Output: []PipelineField{{Name: "list", Type: FieldList}}},
		{Name: "fill", Kind: StageFanout, FanOver: "gaps.list", Body: []PipelineStage{
			{Name: "run", Kind: StageMachine, Machine: "Gap filler", Prompt: "fill {item}"},
		}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("a fanout body of child runs should validate: %v", err)
	}
	rec := &recorder{reply: func(string, string, int) string { return `{"list": ["one", "two"]}` }}
	var ran []string
	out, _, err := new(AppCore).RunPipelineDefHooks(context.Background(), def, "seed", PipelineHooks{
		Dispatch: rec.fn,
		Machine: func(_ context.Context, _, input string) (string, map[string]any, error) {
			ran = append(ran, input)
			return "filled: " + input, nil, nil
		},
	})
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(ran) != 2 {
		t.Fatalf("want one child run per item, got %v", ran)
	}
	if !strings.Contains(out, "filled: fill one") || !strings.Contains(out, "filled: fill two") {
		t.Errorf("each branch's own run should show in the joined block:\n%s", out)
	}
}
