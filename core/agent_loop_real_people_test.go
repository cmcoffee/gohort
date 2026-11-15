package core

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/prompts"
)

// A verdict on whether a named person did something wrong waits for a source
// read this turn. The rule reaches every agent that has tools, beside its
// sibling that covers disagreeing with the user, and an agent with no tools is
// not told to read sources it cannot reach.
func TestAnAgentWithToolsIsToldToReadBeforeJudgingAPerson(t *testing.T) {
	probe := AgentToolDef{
		Tool:    Tool{Name: "probe", Description: "d"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil },
	}
	stub := &FakeLLM{Turns: []FakeTurn{{Content: "done", Repeat: true}}}
	app := &AppCore{LLM: stub}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 2}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	sys := stub.Config(0).SystemPrompt
	rule := prompts.RealPeopleClause()
	at := strings.Index(sys, rule)
	if at < 0 {
		t.Fatalf("the rule never reached the prompt:\n%.400s", sys)
	}
	if d := strings.Index(sys, prompts.DisagreeingClause()); d < 0 || d > at {
		t.Error("the rule should follow the one on disagreeing with the user")
	}
	for _, want := range []string{"THIS turn", "not a starting point", "which person you mean", "Name only sources you actually read", "give your view plainly"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the rule lost %q", want)
		}
	}

	bare := &FakeLLM{Turns: []FakeTurn{{Content: "done", Repeat: true}}}
	if _, _, err := (&AppCore{LLM: bare}).RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{MaxRounds: 1}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if strings.Contains(bare.Config(0).SystemPrompt, "[Claims about real people") {
		t.Error("an agent with no tools was told to read a source")
	}
}
