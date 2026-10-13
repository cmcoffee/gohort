package core

import (
	"context"
	"strings"
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
			Handler: func(context.Context, map[string]any) (string, error) {
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
			Handler: func(context.Context, map[string]any) (string, error) { ran = true; return "ok", nil },
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

// AskFirst widens the gate to a tool that asks nothing on its own: the owner's
// "ask me before every call" on a search reaches Confirm, and a no stops it.
// A tool AskFirst does not name runs as before.
func TestAskFirstGatesAToolThatDeclaresNothing(t *testing.T) {
	app, _ := withTierStubs(t, "test.askfirst", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "search", Args: map[string]any{"q": "x"}},
				{ID: "2", Name: "peek", Args: map[string]any{}},
			}
		}
		return nil
	})
	var mu sync.Mutex
	var asked []string
	ran := map[string]bool{}
	tool := func(name string) AgentToolDef {
		return AgentToolDef{
			Tool: Tool{Name: name, Description: name, Parameters: map[string]ToolParam{"q": {Type: "string"}}},
			Handler: func(context.Context, map[string]any) (string, error) {
				mu.Lock()
				ran[name] = true
				mu.Unlock()
				return "ok", nil
			},
		}
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		RouteKey:  "test.askfirst",
		MaxRounds: 3,
		AskFirst:  func(name string) bool { return name == "search" },
		Confirm: func(name, args string) bool {
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			return false
		},
		Tools: []AgentToolDef{tool("search"), tool("peek")},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != "search" {
		t.Fatalf("Confirm was called for %v, want exactly [search]", asked)
	}
	if ran["search"] || !ran["peek"] {
		t.Errorf("ran = %v: the asked tool was denied and the other one runs", ran)
	}
}

// A no that means "not answered yet" is told to the model in the caller's
// words (ConfirmRefusal), not as a person denying the call.
func TestConfirmRefusalReplacesTheDenial(t *testing.T) {
	app, _ := withTierStubs(t, "test.confirmrefusal", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "1", Name: "search", Args: map[string]any{"q": "x"}}}
		}
		return nil
	})
	search := AgentToolDef{
		Tool:    Tool{Name: "search", Description: "search", Parameters: map[string]ToolParam{"q": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil },
	}
	_, hist, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		RouteKey:       "test.confirmrefusal",
		MaxRounds:      3,
		AskFirst:       func(string) bool { return true },
		Confirm:        func(string, string) bool { return false },
		ConfirmRefusal: func(name string) string { return "NOT RUN YET: waiting on the owner for " + name },
		Tools:          []AgentToolDef{search},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	var seen string
	for _, m := range hist {
		for _, r := range m.ToolResults {
			seen += r.Content
		}
		seen += m.Content
	}
	if !strings.Contains(seen, "NOT RUN YET: waiting on the owner for search") || strings.Contains(seen, "denied by user") {
		t.Errorf("the model was not told it is waiting: %q", seen)
	}
}
