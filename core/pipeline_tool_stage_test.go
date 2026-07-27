package core

// The tool stage: call a tool directly with author-written arguments, no
// LLM in the loop. This is the escape hatch that keeps the stage
// vocabulary from growing one kind per app — deterministic work
// (arithmetic, dedup, normalization) belongs in a tool, not a prompt.

import (
	"context"
	"strings"
	"testing"
)

// calcTool is a stand-in for any deterministic tool: it echoes the args
// it was handed so a test can assert what the stage actually passed.
func calcTool(seen *[]map[string]any, reply string) []AgentToolDef {
	return []AgentToolDef{{
		Tool: Tool{Name: "calculate", Parameters: map[string]ToolParam{"expr": {Type: "string"}}},
		Handler: func(args map[string]any) (string, error) {
			*seen = append(*seen, args)
			return reply, nil
		},
	}}
}

func TestValidate_ToolStage(t *testing.T) {
	ok := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args: map[string]string{"expr": "{stage:plan.expr}"}},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid tool stage rejected: %v", err)
	}

	bad := map[string]PipelineDef{
		"no tool named": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Args: map[string]string{"expr": "1+1"}},
		}},
		"prompt on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Prompt: "compute this"},
		}},
		"think on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Think: boolPtr(true)},
		}},
		"model on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Model: "lead"},
		}},
		"agent on a tool stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate", Agent: "helper"},
		}},
		// An arg reference gets the same forward/unknown check a prompt does.
		"arg references a later stage": {Stages: []PipelineStage{
			{Name: "math", Kind: StageTool, Tool: "calculate",
				Args: map[string]string{"expr": "{stage:plan.expr}"}},
			{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
		}},
		"arg references an undeclared field": {Stages: []PipelineStage{
			{Name: "plan", Prompt: "x", Output: []PipelineField{{Name: "expr", Type: FieldString}}},
			{Name: "math", Kind: StageTool, Tool: "calculate",
				Args: map[string]string{"expr": "{stage:plan.nope}"}},
		}},
		"tool/args on a non-tool stage": {Stages: []PipelineStage{
			{Name: "w", Prompt: "x", Tool: "calculate"},
		}},
	}
	for name, def := range bad {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a validation error, got nil", name)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestToolStage_CallsWithTemplatedArgs(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "plan", Kind: StageAgent, Agent: "planner", Prompt: "plan {input}",
			Output: []PipelineField{{Name: "expr", Type: FieldString, Required: true}}},
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args: map[string]string{"expr": "{stage:plan.expr}", "note": "for {input}"}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	var seen []map[string]any
	rec := &recorder{reply: func(_, _ string, _ int) string { return `{"expr": "2+2"}` }}
	app := &AppCore{}
	out, err := app.executePipelineDef(context.Background(), def, "budget", rec.fn, nil, calcTool(&seen, "4"))
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected one tool call, got %d", len(seen))
	}
	// The field an LLM stage declared feeds the tool call directly — no
	// model in between deciding what to pass.
	if seen[0]["expr"] != "2+2" {
		t.Errorf("expr arg = %v, want the templated field value", seen[0]["expr"])
	}
	if seen[0]["note"] != "for budget" {
		t.Errorf("note arg = %v, want {input} substituted", seen[0]["note"])
	}
	if out != "4" {
		t.Errorf("stage output should be the tool result, got %q", out)
	}
}

func TestToolStage_MissingToolIsAClearError(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate", Args: map[string]string{"expr": "1+1"}},
	}}
	app := &AppCore{}
	var seen []map[string]any
	// Caller has a catalog, just not this tool — the error should say so
	// and list what IS available.
	_, err := app.executePipelineDef(context.Background(), def, "x", nil, nil,
		[]AgentToolDef{{Tool: Tool{Name: "web_search"}, Handler: func(map[string]any) (string, error) { return "", nil }}})
	if err == nil {
		t.Fatal("expected an error for a tool the caller doesn't have")
	}
	if !strings.Contains(err.Error(), "web_search") {
		t.Errorf("error should list the available tools: %v", err)
	}
	// And with no catalog at all, say THAT rather than listing nothing.
	_, err = app.executePipelineDef(context.Background(), def, "x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no tool catalog") {
		t.Errorf("empty-catalog error should be distinct: %v", err)
	}
	_ = seen
}

func TestToolStage_DeclaredOutputDecodesTheResult(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args:   map[string]string{"expr": "2+2"},
			Output: []PipelineField{{Name: "value", Type: FieldNumber, Required: true}}},
		{Name: "say", Kind: StageAgent, Agent: "w", Prompt: "the answer is {stage:math.value}"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("def should validate: %v", err)
	}
	var seen []map[string]any
	rec := &recorder{reply: func(_, _ string, _ int) string { return "done" }}
	app := &AppCore{}
	_, err := app.executePipelineDef(context.Background(), def, "x", rec.fn, nil, calcTool(&seen, `{"value": 4}`))
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if !strings.Contains(rec.prompts[0], "the answer is 4") {
		t.Errorf("the tool's decoded field should template downstream: %q", rec.prompts[0])
	}
}

func TestToolStage_DeclaredOutputMismatchFails(t *testing.T) {
	// No model ran, so there is nothing to repair — a mismatch means the
	// tool's contract is wrong, and that should surface rather than retry.
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate",
			Args:   map[string]string{"expr": "2+2"},
			Output: []PipelineField{{Name: "value", Type: FieldNumber, Required: true}}},
	}}
	var seen []map[string]any
	app := &AppCore{}
	_, err := app.executePipelineDef(context.Background(), def, "x", nil, nil, calcTool(&seen, "not json at all"))
	if err == nil {
		t.Fatal("expected an error when the tool result doesn't match the declared output")
	}
	if len(seen) != 1 {
		t.Errorf("the tool should have been called exactly once (no repair retry), got %d", len(seen))
	}
}
