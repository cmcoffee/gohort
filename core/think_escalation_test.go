package core

import (
	"context"
	"testing"
)

// applyOptsFor collects what a round actually asked the client for, so a test can
// assert on the resolved config rather than on the option slice's shape.
func applyOptsFor(opts []ChatOption) ChatConfig {
	var cfg ChatConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// A turn still going at round N is demonstrably multi-step. That is evidence the
// turn produced — no model self-report, and no wasted round-trip spent asking.
func TestThinkEscalatesAfterRoundCount(t *testing.T) {
	var perRound []ChatConfig
	stub := &tierStubLLM{name: "worker", log: &[]string{}, reply: func(n int) []ToolCall {
		if n < 6 {
			return []ToolCall{{ID: "1", Name: "noop", Args: map[string]any{}}}
		}
		return nil
	}}
	stub.onOpts = func(o []ChatOption) { perRound = append(perRound, applyOptsFor(o)) }

	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(stub, stub)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	RegisterRouteStage(RouteStage{Key: "test.escalate", Label: "test", Default: "worker"})
	app := &AppCore{LLM: stub, LeadLLM: stub}

	noop := AgentToolDef{
		Tool:    Tool{Name: "noop", Description: "does nothing", Parameters: map[string]ToolParam{}},
		Handler: func(args map[string]any) (string, error) { return "ok", nil },
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:           []AgentToolDef{noop},
		MaxRounds:       10,
		RouteKey:        "test.escalate",
		ThinkEscalation: ThinkEscalation{AfterRound: 3, Budget: 2048},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(perRound) < 5 {
		t.Fatalf("need several rounds to observe the switch; got %d", len(perRound))
	}
	// Rounds 1-3 untouched.
	for i := 0; i < 3; i++ {
		if perRound[i].ThinkBudget != nil && *perRound[i].ThinkBudget == 2048 {
			t.Errorf("round %d escalated early — a short turn must pay nothing", i+1)
		}
	}
	// Round 4 onward carries BOTH. The budget alone would be discarded:
	// llamacppThinkBudget short-circuits on a no-think call and never reads it.
	got := perRound[3]
	if got.Think == nil || !*got.Think {
		t.Error("escalation must set Think(true), or the budget is silently dropped on a no-think agent")
	}
	if got.ThinkBudget == nil || *got.ThinkBudget != 2048 {
		t.Errorf("escalated round must carry the budget; got %v", got.ThinkBudget)
	}
}

// Zero value is off: a caller that says nothing gets exactly what it got before.
func TestThinkEscalationOffByDefault(t *testing.T) {
	var e ThinkEscalation
	for round := 1; round <= 20; round++ {
		if e.Engaged(round) {
			t.Fatalf("the zero value must never engage; fired on round %d", round)
		}
	}
	// A half-configured rule is also off — a budget with no threshold, or a
	// threshold with no budget, is a mistake rather than an instruction.
	if (ThinkEscalation{AfterRound: 3}).Engaged(9) {
		t.Error("a threshold with no budget must not engage")
	}
	if (ThinkEscalation{Budget: 2048}).Engaged(9) {
		t.Error("a budget with no threshold must not engage")
	}
}

// Escalation begins on the round AFTER the threshold, so AfterRound reads as
// "rounds completed before it applies".
func TestThinkEscalationBoundary(t *testing.T) {
	e := ThinkEscalation{AfterRound: 3, Budget: 512}
	for _, r := range []int{1, 2, 3} {
		if e.Engaged(r) {
			t.Errorf("round %d is within the threshold and must not escalate", r)
		}
	}
	for _, r := range []int{4, 5, 99} {
		if !e.Engaged(r) {
			t.Errorf("round %d is past the threshold and must escalate", r)
		}
	}
}
