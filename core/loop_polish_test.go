package core

import (
	"context"
	"fmt"
	"testing"
)

// A think-tag leak in the final content (upstream reasoning/content split
// failure) is stripped at the loop boundary; an answer that merely MENTIONS
// think tags inside code markup is left alone.
func TestThinkTagLeakStrippedAtLoopBoundary(t *testing.T) {
	leak := "blocks\n\n6. For simple queries, skip thinking.\n</think>\n\nQwen works well without thinking."
	app, _ := withTierStubs(t, "test.thinkstrip", func(n int) []ToolCall { return nil })
	app.LLM.(*tierStubLLM).content = leak
	app.LeadLLM.(*tierStubLLM).content = leak
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "q"}}, AgentLoopConfig{
		MaxRounds: 2, RouteKey: "test.thinkstrip",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if resp.Content != "Qwen works well without thinking." {
		t.Fatalf("leak should be stripped to the answer side, got %q", resp.Content)
	}

	// Mention inside backticks — untouched.
	mention := "Reasoning traces live inside `<think>` blocks; the closer is `</think>`."
	app2, _ := withTierStubs(t, "test.thinkstrip2", func(n int) []ToolCall { return nil })
	app2.LLM.(*tierStubLLM).content = mention
	app2.LeadLLM.(*tierStubLLM).content = mention
	resp2, _, err := app2.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "q"}}, AgentLoopConfig{
		MaxRounds: 2, RouteKey: "test.thinkstrip2",
	})
	if err != nil {
		t.Fatalf("loop2: %v", err)
	}
	if resp2.Content != mention {
		t.Fatalf("code-span mention must not be clipped, got %q", resp2.Content)
	}
}

// After a repeat guard trips, the NEXT LLM call carries the one-shot
// shake-out temperature; steady-state rounds carry none.
func TestShakeoutTemperatureAfterGuardTrip(t *testing.T) {
	var temps []*float64
	app, _ := withTierStubs(t, "test.shakeout", func(n int) []ToolCall {
		if n >= 8 {
			return nil
		}
		// Identical failing call every round — trips the repeat-fail guard.
		return []ToolCall{{ID: fmt.Sprint(n), Name: "probe", Args: map[string]any{"when": "always-same"}}}
	})
	rec := func(opts []ChatOption) {
		cfg := &ChatConfig{}
		for _, o := range opts {
			o(cfg)
		}
		temps = append(temps, cfg.Temperature)
	}
	app.LLM.(*tierStubLLM).onOpts = rec
	app.LeadLLM.(*tierStubLLM).onOpts = rec

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools:     []AgentToolDef{alwaysFailTool("probe", "upstream refused the connection outright")},
		MaxRounds: 8,
		RouteKey:  "test.shakeout",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(temps) < 4 {
		t.Fatalf("expected several rounds, got %d", len(temps))
	}
	// Round 1 must be un-shaken; some later round must carry 0.9.
	if temps[0] != nil {
		t.Errorf("first round must not carry the shake-out temperature")
	}
	shaken := false
	for _, tp := range temps {
		if tp != nil && *tp == shakeoutTemperature {
			shaken = true
		}
	}
	if !shaken {
		t.Errorf("no round carried the shake-out temperature after guard trips (temps=%v)", temps)
	}
}
