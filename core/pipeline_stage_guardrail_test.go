package core

// Guardrails reaching INSIDE a pipeline. The dispatch boundary judges what a
// pipeline says; these pin what stops it ACTING — the half no output check can
// undo, because by the time there is an output to judge the mail has been sent.

import (
	"context"
	"strings"
	"testing"
)

// blockingGuards refuses every pre_action it is asked about and records which
// hook points reached it, so a test can tell what the narrowing let through.
func blockingGuards(hooks *[]string) StageGuardrails {
	return StageGuardrails{
		Check: func(hookPoint, candidate string) GuardrailDecision {
			*hooks = append(*hooks, hookPoint)
			return GuardrailDecision{Blocked: true, Message: "no"}
		},
		Halted: func() bool { return false },
	}
}

// confirmingTool is a consequential tool — the NeedsConfirm set is exactly what
// the pre_action gate covers, in a stage as in an agent loop.
func confirmingTool(ran *bool) []AgentToolDef {
	return []AgentToolDef{{
		Tool:         Tool{Name: "send_message", Parameters: map[string]ToolParam{"to": {Type: "string"}}},
		NeedsConfirm: true,
		Handler: func(args map[string]any) (string, error) {
			*ran = true
			return "sent", nil
		},
	}}
}

// TestToolStage_PreActionBlocksConsequentialTool is the whole point: a stage
// that would act runs the caller's warden first, and a block means the handler
// is never reached — not that its result is discarded afterwards.
func TestToolStage_PreActionBlocksConsequentialTool(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "notify", Kind: StageTool, Tool: "send_message", Args: map[string]string{"to": "{input}"}},
	}}
	var ran bool
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	_, err := (&AppCore{}).executePipelineDef(ctx, def, "customer@example.com", nil, nil, confirmingTool(&ran))
	if err == nil {
		t.Fatal("a blocked pre_action must fail the stage")
	}
	if ran {
		t.Fatal("the tool RAN — an action gate that only judges after the call is not a gate")
	}
	if len(hooks) != 1 || hooks[0] != GuardHookPreAction {
		t.Fatalf("the stage must consult pre_action exactly once; got %v", hooks)
	}
	// Names no rule and no mechanism, same line every block message here holds.
	for _, banned := range []string{"guardrail", "warden", "policy"} {
		if strings.Contains(strings.ToLower(err.Error()), banned) {
			t.Errorf("the stage error must not name the mechanism (%q): %v", banned, err)
		}
	}
}

// TestToolStage_OrdinaryToolIsNotJudged pins the cost side: a read-only tool is
// outside the consequential set, so it costs no warden call at all.
func TestToolStage_OrdinaryToolIsNotJudged(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "math", Kind: StageTool, Tool: "calculate", Args: map[string]string{"expr": "2+2"}},
	}}
	var seen []map[string]any
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	out, err := (&AppCore{}).executePipelineDef(ctx, def, "x", nil, nil, calcTool(&seen, "4"))
	if err != nil {
		t.Fatalf("a non-consequential tool must not be gated: %v", err)
	}
	if out != "4" || len(seen) != 1 {
		t.Fatalf("the tool should have run normally; out=%q calls=%d", out, len(seen))
	}
	if len(hooks) != 0 {
		t.Fatalf("a read-only tool must cost no warden call; got %v", hooks)
	}
}

// TestStageGuardrails_OnlyPreActionReaches pins the narrowing. A stage's agent
// loop offers every hook point; only pre_action is answered, because a stage's
// text is judged once as part of the pipeline's output rather than per stage.
func TestStageGuardrails_OnlyPreActionReaches(t *testing.T) {
	var hooks []string
	check := blockingGuards(&hooks).stageCheck()
	for _, hp := range []string{GuardHookPreOutput, GuardHookPeriodic, GuardHookPreInput} {
		if check(hp, "anything").Blocked {
			t.Errorf("%s must not fire inside a stage", hp)
		}
	}
	if len(hooks) != 0 {
		t.Fatalf("a narrowed-away hook must not even reach the warden; got %v", hooks)
	}
	if !check(GuardHookPreAction, "send_message to=x").Blocked {
		t.Fatal("pre_action must still fire")
	}
}

// TestStageGuardrails_DroppedAtAgentStage pins the hand-off: an agent stage
// runs an agent that has rules of its own, and judging its actions by the
// caller's is not what either owner authored.
func TestStageGuardrails_DroppedAtAgentStage(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "ask", Kind: StageAgent, Agent: "helper", Prompt: "{input}"},
	}}
	var carried bool
	dispatch := func(ctx context.Context, agentID, in string) (string, error) {
		carried = stageGuardrails(ctx).Check != nil
		return "done", nil
	}
	var hooks []string
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	if _, err := (&AppCore{}).executePipelineDef(ctx, def, "x", dispatch, nil, nil); err != nil {
		t.Fatalf("agent stage failed: %v", err)
	}
	if carried {
		t.Fatal("the caller's enforcement set must not travel into a dispatched agent")
	}
}

// TestStageGuardrails_InertSetIsNotCarried pins that an agent with no rules
// puts nothing on the context — core keeps its no-guardrails fast path.
func TestStageGuardrails_InertSetIsNotCarried(t *testing.T) {
	ctx := WithStageGuardrails(context.Background(), StageGuardrails{})
	if stageGuardrails(ctx).Check != nil {
		t.Fatal("an inert set must not be carried")
	}
}

// callThenAnswerLLM asks for a consequential tool on the first round and writes
// a reply on the second — the ordinary shape of a worker stage that acts.
type callThenAnswerLLM struct{ n int }

func (s *callThenAnswerLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	s.n++
	if s.n == 1 {
		return &Response{ToolCalls: []ToolCall{{ID: "1", Name: "send_message", Args: map[string]any{"to": "customer@example.com"}}}}, nil
	}
	return &Response{Content: "done what I could"}, nil
}
func (s *callThenAnswerLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// TestWorkerStage_PreActionBlocksToolCall is the same guarantee one layer up:
// the tool a worker stage's own model chooses to call is judged too. Blocking
// it does NOT fail the stage — the loop hands the refusal back as the tool
// result and the stage finishes, exactly as it does in a full agent turn.
func TestWorkerStage_PreActionBlocksToolCall(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "reach_out", Kind: StageWorker, Prompt: "contact {input}", Tools: []string{"send_message"}},
	}}
	var ran bool
	var hooks []string
	app := &AppCore{LLM: &callThenAnswerLLM{}}
	ctx := WithStageGuardrails(context.Background(), blockingGuards(&hooks))
	out, err := app.executePipelineDef(ctx, def, "customer@example.com", nil, nil, confirmingTool(&ran))
	if err != nil {
		t.Fatalf("a blocked action must not sink the stage: %v", err)
	}
	if ran {
		t.Fatal("the tool RAN — the stage loop was handed no pre_action gate")
	}
	if len(hooks) == 0 || hooks[0] != GuardHookPreAction {
		t.Fatalf("the stage loop must consult pre_action; got %v", hooks)
	}
	// And only pre_action: the stage's own text is judged once at the pipeline
	// boundary, not stage by stage.
	for _, hp := range hooks {
		if hp != GuardHookPreAction {
			t.Errorf("no hook but pre_action may fire inside a stage; got %s", hp)
		}
	}
	if out == "" {
		t.Error("the stage should still produce its reply after a blocked call")
	}
}

// TestWorkerStage_UngovernedStageCostsNothing pins the fast path: no set on the
// context means the loop gets a nil check and the tool runs untouched.
func TestWorkerStage_UngovernedStageCostsNothing(t *testing.T) {
	def := PipelineDef{Stages: []PipelineStage{
		{Name: "reach_out", Kind: StageWorker, Prompt: "contact {input}", Tools: []string{"send_message"}},
	}}
	var ran bool
	app := &AppCore{LLM: &callThenAnswerLLM{}}
	if _, err := app.executePipelineDef(context.Background(), def, "x", nil, nil, confirmingTool(&ran)); err != nil {
		t.Fatalf("ungoverned stage failed: %v", err)
	}
	if !ran {
		t.Fatal("with no guardrails the tool must run — the gate is not supposed to be on by default")
	}
}
