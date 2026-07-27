package core

// Per-stage model tier. The validation half matters more than the
// execution half here: a tier silently dropped on a kind it can't apply
// to reads as "I asked for lead and got worker", which is
// indistinguishable from a routing bug — so those are rejected at save
// time rather than ignored.

import "testing"

func TestValidate_StageModel(t *testing.T) {
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Prompt: "decompose {input}", Model: "lead"},
		{Name: "fetch", Kind: StageFanout, FanOver: "plan", Prompt: "{item}", Model: "worker"},
		{Name: "synth", Kind: StageSynthesize, Prompt: "combine {prev}", Model: "LEAD"},
		{Name: "plain", Prompt: "no tier at all"},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid model tiers rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"unknown tier": {Stages: []PipelineStage{
			{Name: "a", Prompt: "x", Model: "gpt-5"},
		}},
		"on an agent stage": {Stages: []PipelineStage{
			{Name: "a", Kind: StageAgent, Agent: "helper", Prompt: "x", Model: "lead"},
		}},
		"on a branch": {Stages: []PipelineStage{
			{Name: "f", Prompt: "x", Output: []PipelineField{{Name: "b", Type: FieldBool}}},
			{Name: "g", Kind: StageBranch, When: "f.b", Model: "lead"},
		}},
		"on the loop itself": {Stages: []PipelineStage{
			{Name: "rounds", Kind: StageLoop, Count: 2, Model: "lead",
				Body: []PipelineStage{{Name: "step", Prompt: "x"}}},
		}},
		"on an agent-dispatching fanout": {Stages: []PipelineStage{
			{Name: "plan", Prompt: "x"},
			{Name: "fan", Kind: StageFanout, FanOver: "plan", Agent: "helper", Prompt: "{item}", Model: "lead"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}

	// A loop's BODY stages carry the tier — that's the supported form.
	inBody := PipelineDef{Stages: []PipelineStage{
		{Name: "rounds", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "step", Prompt: "x", Model: "lead"},
		}},
	}}
	if err := inBody.Validate(); err != nil {
		t.Errorf("model on a loop body stage should be allowed: %v", err)
	}
}

func TestStageTier(t *testing.T) {
	cases := map[string]LLMTier{
		"":       WORKER,
		"worker": WORKER,
		"lead":   LEAD,
		"LEAD":   LEAD,
		" lead ": LEAD,
	}
	for in, want := range cases {
		if got := stageTier(PipelineStage{Model: in}); got != want {
			t.Errorf("stageTier(%q) = %v, want %v", in, got, want)
		}
	}
}
