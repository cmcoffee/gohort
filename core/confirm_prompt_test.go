package core

import (
	"context"
	"sync"
	"testing"
)

// A tool that declares only a Confirmation — no NeedsConfirm — must still be
// gated. The two would be a trap otherwise: the author writes the sentence the
// user is meant to read, the loop never calls Confirm, and the tool runs
// unasked with a prompt that nothing ever renders.
func TestConfirmationAloneGatesTheCall(t *testing.T) {
	app, _ := withTierStubs(t, "test.confirmprompt", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "1", Name: "wipe", Args: map[string]any{"path": "x"}}}
		}
		return nil
	})

	var mu sync.Mutex
	var asked []string
	ran := false

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		RouteKey:  "test.confirmprompt",
		MaxRounds: 3,
		Confirm: func(name, args string) bool {
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			return false // deny, so the handler must not run
		},
		Tools: []AgentToolDef{{
			Tool:         Tool{Name: "wipe", Description: "wipe", Parameters: map[string]ToolParam{"path": {Type: "string"}}},
			Confirmation: &ToolConfirmation{Prompt: "Really wipe it?"}, // NeedsConfirm deliberately unset
			Handler: func(map[string]any) (string, error) {
				ran = true
				return "done", nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "wipe" {
		t.Fatalf("Confirm was called for %v, want exactly [wipe]", asked)
	}
	if ran {
		t.Fatal("the handler ran despite the confirmation being denied")
	}
}

// The converse: a tool that declares neither must not start prompting. This is
// the promise that adding the field changed nothing for existing tools.
func TestToolWithoutConfirmFieldsIsNotGated(t *testing.T) {
	app, _ := withTierStubs(t, "test.noconfirm", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "1", Name: "peek", Args: map[string]any{}}}
		}
		return nil
	})

	asked := false
	ran := false
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		RouteKey:  "test.noconfirm",
		MaxRounds: 3,
		Confirm:   func(name, args string) bool { asked = true; return true },
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "peek", Description: "peek", Parameters: map[string]ToolParam{}},
			Handler: func(map[string]any) (string, error) { ran = true; return "ok", nil },
		}},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if asked {
		t.Fatal("a tool declaring neither field was put behind a prompt")
	}
	if !ran {
		t.Fatal("the tool did not run")
	}
}
