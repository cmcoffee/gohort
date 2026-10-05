package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's own loop gets orchestrate's guardrail hooks, and its tools the
// tool-result policy, from a guard built without any chat turn behind it:
// no session, no stream, and here no model either. None of that may panic
// or block an ordinary call.
func TestAppLoopGuardAppliesWithoutATurn(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-own-loop", Name: "Own Loop", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	if (*AppLoopGuard)(nil).Apply(AgentLoopConfig{MaxRounds: 3}).MaxRounds != 3 {
		t.Fatal("a nil guard changed the config")
	}
	if T.AppLoopGuard(context.Background(), "u", "no-such-agent") != nil {
		t.Fatal("a guard was built for an agent that does not exist")
	}
	g := T.AppLoopGuard(context.Background(), "u", "app-test-own-loop")
	if g == nil {
		t.Fatal("no guard for an app agent")
	}
	calls := 0
	fetch := AgentToolDef{
		Tool: Tool{Name: "fetch_thing", Caps: []Capability{CapNetwork}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			calls++
			return "the page text", nil
		},
	}
	orig := []AgentToolDef{fetch}
	cfg := g.Apply(AgentLoopConfig{Tools: orig, MaxRounds: 5})
	if cfg.MaxRounds != 5 || cfg.GuardrailCheck == nil || cfg.GuardrailActionGate == nil || cfg.GuardrailReject == nil {
		t.Fatalf("hooks not set: %+v", cfg.GuardrailCheck == nil)
	}
	out, err := cfg.Tools[0].Handler(context.Background(), map[string]any{})
	if err != nil || calls != 1 || !strings.Contains(out, "the page text") || out == "the page text" {
		t.Fatalf("wrapped call = %q, %v (calls %d): want the text, fenced", out, err, calls)
	}
	if orig[0].Handler == nil || &orig[0] == &cfg.Tools[0] {
		t.Fatal("Apply mutated the caller's tools")
	}
	if dec := cfg.GuardrailCheck("pre_output", "Here is what I found."); dec.Blocked {
		t.Fatalf("an ordinary reply was blocked: %+v", dec)
	}
}
