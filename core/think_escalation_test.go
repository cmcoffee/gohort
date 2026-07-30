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
		ThinkEscalation: ThinkEscalation{AfterRound: 3, Budget: 2048}, // round trigger only
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
		if e.Engaged(round, 0) {
			t.Fatalf("the zero value must never engage; fired on round %d", round)
		}
	}
	// A budget with no threshold is a mistake, not an instruction — there is no
	// round at which it would ever apply.
	if (ThinkEscalation{Budget: 2048}).Engaged(9, 0) {
		t.Error("a budget with no threshold must not engage")
	}
	// A threshold with NO budget is the normal configuration, not a broken one:
	// it means "lift the suppression and use whatever budget is already
	// configured", which is what makes escalation agree with the admin UI instead
	// of carrying a second number that disagrees with it.
	if !(ThinkEscalation{AfterRound: 3}).Engaged(9, 0) {
		t.Error("a threshold alone must engage — a zero budget means 'use the configured one'")
	}
}

// The default shape: escalation lifts thinking and leaves the AMOUNT to the
// existing per-agent / per-route / global layering, so an escalated turn thinks
// with exactly what the admin UI configured rather than a number hidden in code.
func TestThinkEscalationDefersToConfiguredBudget(t *testing.T) {
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
	RegisterRouteStage(RouteStage{Key: "test.escdefer", Label: "test", Default: "worker"})
	app := &AppCore{LLM: stub, LeadLLM: stub}

	noop := AgentToolDef{
		Tool:    Tool{Name: "noop", Description: "does nothing", Parameters: map[string]ToolParam{}},
		Handler: func(args map[string]any) (string, error) { return "ok", nil },
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 10,
		RouteKey:  "test.escdefer",
		// The agent's own configured budget. Escalation must not replace it.
		ThinkBudget:     777,
		ThinkEscalation: ThinkEscalation{AfterRound: 3}, // no budget override
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(perRound) < 5 {
		t.Fatalf("need several rounds; got %d", len(perRound))
	}
	got := perRound[3]
	if got.Think == nil || !*got.Think {
		t.Error("escalation must lift the suppression with Think(true)")
	}
	if got.ThinkBudget == nil || *got.ThinkBudget != 777 {
		t.Errorf("an escalated round must keep the CONFIGURED budget, not a hidden default; got %v", got.ThinkBudget)
	}
}

// Escalation begins on the round AFTER the threshold, so AfterRound reads as
// "rounds completed before it applies".
func TestThinkEscalationBoundary(t *testing.T) {
	e := ThinkEscalation{AfterRound: 3, Budget: 512}
	for _, r := range []int{1, 2, 3} {
		if e.Engaged(r, 0) {
			t.Errorf("round %d is within the threshold and must not escalate", r)
		}
	}
	for _, r := range []int{4, 5, 99} {
		if !e.Engaged(r, 0) {
			t.Errorf("round %d is past the threshold and must escalate", r)
		}
	}
}

// The sharper trigger. Calling a tool is the turn saying it is doing work rather
// than answering, and the round AFTER the call is where results get synthesised —
// which is where reasoning earns its cost. Waiting for a round count gets this
// backwards for a planner: the plan is made in round one, so a threshold of three
// delivers thinking after the decisions that needed it.
func TestThinkEscalatesOnToolUse(t *testing.T) {
	var perRound []ChatConfig
	stub := &tierStubLLM{name: "worker", log: &[]string{}, reply: func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "1", Name: "noop", Args: map[string]any{}}}
		}
		return nil // round 2 synthesises and finishes
	}}
	stub.onOpts = func(o []ChatOption) { perRound = append(perRound, applyOptsFor(o)) }

	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(stub, stub)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	RegisterRouteStage(RouteStage{Key: "test.esctool", Label: "test", Default: "worker"})
	app := &AppCore{LLM: stub, LeadLLM: stub}

	noop := AgentToolDef{
		Tool:    Tool{Name: "noop", Description: "does nothing", Parameters: map[string]ToolParam{}},
		Handler: func(args map[string]any) (string, error) { return "ok", nil },
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "look it up"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{noop},
		MaxRounds: 6,
		RouteKey:  "test.esctool",
		// Round trigger deliberately far out of reach: only the tool signal can fire.
		ThinkEscalation: ThinkEscalation{AfterRound: 99, AfterToolRounds: 1},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(perRound) < 2 {
		t.Fatalf("expected a tool round then a synthesis round; got %d", len(perRound))
	}
	if perRound[0].Think != nil && *perRound[0].Think {
		t.Error("the first round has shown nothing yet and must stay cheap")
	}
	if perRound[1].Think == nil || !*perRound[1].Think {
		t.Error("the round that synthesises tool results is exactly where reasoning should land")
	}
}

// A one-shot reply never trips it, which is the whole point: short conversational
// turns must not pay for reasoning they do not use.
func TestNoEscalationForAOneShotReply(t *testing.T) {
	var perRound []ChatConfig
	stub := &tierStubLLM{name: "worker", log: &[]string{}, reply: func(n int) []ToolCall { return nil }}
	stub.onOpts = func(o []ChatOption) { perRound = append(perRound, applyOptsFor(o)) }

	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(stub, stub)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	RegisterRouteStage(RouteStage{Key: "test.esconeshot", Label: "test", Default: "worker"})
	app := &AppCore{LLM: stub, LeadLLM: stub}

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hello"}}, AgentLoopConfig{
		MaxRounds:       6,
		RouteKey:        "test.esconeshot",
		ThinkEscalation: ThinkEscalation{AfterRound: 3, AfterToolRounds: 1},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	for i, c := range perRound {
		if c.Think != nil && *c.Think {
			t.Errorf("round %d escalated on a one-shot reply — short turns must stay cheap", i+1)
		}
	}
}
