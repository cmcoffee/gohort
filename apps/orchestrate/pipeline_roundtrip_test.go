package orchestrate

// The authoring surface for loop: a nested body has to survive the tool's
// parser on create AND update. A field the tool documents but the parser
// drops is the failure shape this package has produced twice.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestParsePipelineStages_LoopRoundTrip(t *testing.T) {
	raw := []any{
		map[string]any{
			"name": "rounds", "kind": "loop",
			"count": float64(3), "until": "check.done", "collect": "all",
			"body": []any{
				map[string]any{"name": "argue", "kind": "agent", "agent": "debater",
					"prompt": "pass {iteration}: rebut {prev}", "think": true},
				map[string]any{"name": "check", "prompt": "settled?",
					"output": []any{map[string]any{"name": "done", "type": "bool", "required": true}}},
			},
		},
	}
	got, err := parsePipelineStages(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(got))
	}
	s := got[0]
	if s.Kind != StageLoop || s.Count != 3 || s.Until != "check.done" || s.Collect != "all" {
		t.Errorf("loop fields dropped: %+v", s)
	}
	if len(s.Body) != 2 {
		t.Fatalf("body dropped: %+v", s.Body)
	}
	// Nested stages keep the fields the flat parser handles, including the
	// two that used to vanish silently.
	if s.Body[0].Agent != "debater" || s.Body[0].Think == nil || !*s.Body[0].Think {
		t.Errorf("body stage fields dropped: %+v", s.Body[0])
	}
	if len(s.Body[1].Output) != 1 || s.Body[1].Output[0].Type != FieldBool {
		t.Errorf("body stage output dropped: %+v", s.Body[1].Output)
	}
	// And the whole thing has to actually validate.
	def := PipelineDef{Name: "t", Stages: got}
	if err := def.Validate(); err != nil {
		t.Errorf("parsed loop should validate: %v", err)
	}
}

func TestParsePipelineStages_LoopCountAsString(t *testing.T) {
	// Models send numbers as strings routinely; coerce rather than drop.
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "rounds", "kind": "loop", "count": "2",
			"body": []any{map[string]any{"name": "step", "prompt": "x"}}},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got[0].Count != 2 {
		t.Errorf("count = %d, want 2", got[0].Count)
	}
}

func TestParsePipelineStages_BranchRoundTrip(t *testing.T) {
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "frame", "prompt": "screen it",
			"output": []any{map[string]any{"name": "rejected", "type": "bool", "required": true}}},
		map[string]any{"name": "gate", "kind": "branch",
			"when": "frame.rejected", "skip_to": "report"},
		map[string]any{"name": "work", "prompt": "do it"},
		map[string]any{"name": "report", "prompt": "wrap up"},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	gate := got[1]
	if gate.Kind != StageBranch || gate.When != "frame.rejected" || gate.SkipTo != "report" {
		t.Errorf("branch fields dropped: %+v", gate)
	}
	if err := (PipelineDef{Name: "t", Stages: got}).Validate(); err != nil {
		t.Errorf("parsed branch should validate: %v", err)
	}
}

func TestParsePipelineStages_ModelTierRoundTrip(t *testing.T) {
	got, err := parsePipelineStages([]any{
		map[string]any{"name": "plan", "prompt": "decompose", "model": "LEAD"},
		map[string]any{"name": "note", "prompt": "transform"},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got[0].Model != "lead" {
		t.Errorf("model = %q, want normalized \"lead\"", got[0].Model)
	}
	if got[1].Model != "" {
		t.Errorf("unset model should stay empty, got %q", got[1].Model)
	}
	if err := (PipelineDef{Name: "t", Stages: got}).Validate(); err != nil {
		t.Errorf("parsed tiers should validate: %v", err)
	}
}
