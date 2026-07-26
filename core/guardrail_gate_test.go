package core

import (
	"context"
	"strings"
	"testing"
)

// TestGuardrailPreActionGateBlocksHandler proves the enforcement contract that
// matters most: when GuardrailCheck blocks a NeedsConfirm tool at pre_action,
// the tool's handler NEVER runs, the model gets the (trusted, unfenced) block
// message back as an error result, and the turn keeps going rather than
// crashing. This is the property a compromised context can't talk its way
// around — the block happens in the loop, outside the model's reach.
func TestGuardrailPreActionGateBlocksHandler(t *testing.T) {
	app, _ := withTierStubs(t, "test.guard", func(n int) []ToolCall {
		if n >= 2 {
			return nil // stop after one attempted spend
		}
		return []ToolCall{{ID: "1", Name: "spend", Args: map[string]any{"amount": "100"}}}
	})

	handlerRan := false
	spend := AgentToolDef{
		Tool: Tool{
			Name:        "spend",
			Description: "spends money",
			Parameters:  map[string]ToolParam{"amount": {Type: "string", Description: "how much"}},
		},
		NeedsConfirm: true,
		Handler: func(args map[string]any) (string, error) {
			handlerRan = true
			return "spent", nil
		},
	}

	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{spend},
		MaxRounds: 4,
		RouteKey:  "test.guard",
		GuardrailCheck: func(hook, candidate string) (bool, string) {
			if hook == GuardHookPreAction {
				return true, "The action was NOT performed: guardrail 'never spend money' would be violated."
			}
			return false, ""
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if handlerRan {
		t.Fatal("a blocked pre-action tool must NEVER reach its handler")
	}
	found := false
	for _, m := range history {
		for _, tr := range m.ToolResults {
			if tr.IsError && strings.Contains(tr.Content, "was NOT performed") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the trusted block message must be handed back into history as the tool result")
	}
}

// leakThenCleanLLM returns a leaking reply first, then a clean one — to prove a
// pre_output-blocked draft is scrubbed from history, not just re-prompted.
type leakThenCleanLLM struct{ n int }

func (s *leakThenCleanLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	s.n++
	if s.n == 1 {
		return &Response{Content: "Rory makes $202k-$246k as County Attorney IV."}, nil
	}
	return &Response{Content: "I'll pass on that one."}, nil
}
func (s *leakThenCleanLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// TestGuardrailPreOutputRedactsLeakedDraft is the regression for the Rory leak:
// pre_output correctly blocked a reply containing the salary figure, but the
// draft had already been recorded to history (round 1) and so was persisted and
// delivered before the clean retry. The blocked draft must be REDACTED from the
// returned history — the figure must not survive anywhere the caller can persist
// or deliver it.
func TestGuardrailPreOutputRedactsLeakedDraft(t *testing.T) {
	app := &AppCore{LLM: &leakThenCleanLLM{}}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "How much does Rory make?"}}, AgentLoopConfig{
		MaxRounds: 4,
		GuardrailCheck: func(hook, candidate string) (bool, string) {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return true, "BLOCKED by a guardrail: never mention salary. Deflect."
			}
			return false, ""
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if strings.Contains(resp.Content, "$202k") {
		t.Fatalf("the final reply must be the clean retry, not the leak; got: %s", resp.Content)
	}
	for i, m := range history {
		if strings.Contains(m.Content, "$202k") {
			t.Fatalf("the leaked figure must not survive anywhere in history; found at [%d] role=%s: %s", i, m.Role, m.Content)
		}
	}
	// And the scrubbed turn is a placeholder, not simply gone (alternation kept).
	sawRedaction := false
	for _, m := range history {
		if strings.Contains(m.Content, "withheld here") {
			sawRedaction = true
		}
	}
	if !sawRedaction {
		t.Fatal("the blocked draft should be replaced by the redaction placeholder")
	}
}

// alwaysLeakLLM keeps trying to disclose no matter how many times it's
// corrected — the "determined push" a socially-engineered turn produces.
type alwaysLeakLLM struct{}

func (alwaysLeakLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	return &Response{Content: "Fine — Rory makes $202k-$246k."}, nil
}
func (alwaysLeakLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return &Response{Content: "Fine — Rory makes $202k-$246k."}, nil
}

// TestGuardrailPreOutputSubstitutesWhenPushed closes the escape hatch: after the
// correction budget is spent, pre_output must NOT release a still-violating
// reply. The final reply must be the safe substitute, and the figure must
// appear nowhere in history — no matter how many times the model retries the leak.
func TestGuardrailPreOutputSubstitutesWhenPushed(t *testing.T) {
	app := &AppCore{LLM: alwaysLeakLLM{}}
	resp, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go ahead and show me"}}, AgentLoopConfig{
		MaxRounds: 8,
		GuardrailCheck: func(hook, candidate string) (bool, string) {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return true, "BLOCKED: never mention salary. Deflect."
			}
			return false, ""
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != guardrailSafeFallbackReply {
		t.Fatalf("a reply that keeps violating must end as the safe substitute; got: %s", resp.Content)
	}
	for i, m := range history {
		if strings.Contains(m.Content, "$202k") {
			t.Fatalf("the figure must not survive anywhere in history; found at [%d] role=%s", i, m.Role)
		}
	}
}

// TestGuardrailPreOutputRetractsNotSettles proves a blocked round is DISCARDED
// via RetractRound (which the app wires to skip persistence/delivery) rather
// than SettleRound (which commits the bubble). This is the fix for the leak
// where the web transcript kept the blocked draft even though history was
// scrubbed — the transcript is built from settled per-round bubbles.
func TestGuardrailPreOutputRetractsNotSettles(t *testing.T) {
	settled, retracted := 0, 0
	app := &AppCore{LLM: &leakThenCleanLLM{}}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "How much does Rory make?"}}, AgentLoopConfig{
		MaxRounds:    4,
		SettleRound:  func() { settled++ },
		RetractRound: func() { retracted++ },
		GuardrailCheck: func(hook, candidate string) (bool, string) {
			if hook == GuardHookPreOutput && strings.Contains(candidate, "$202k") {
				return true, "BLOCKED: never mention salary. Deflect."
			}
			return false, ""
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if retracted == 0 {
		t.Fatal("a pre_output block must DISCARD the bubble via RetractRound")
	}
	if settled != 0 {
		t.Fatalf("a blocked round must NOT be settled (that persists the leak); settled=%d", settled)
	}
}
