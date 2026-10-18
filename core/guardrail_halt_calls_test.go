package core

import (
	"context"
	"reflect"
	"testing"
)

// A halted turn's reply writer is told what the turn's tools did, so a run
// nobody asked for can report its work instead of declining its mission.
func TestHaltedTurnCallsListsThisTurnsCalls(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "an earlier turn"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "old_call"}}},
		{Role: "user", Content: "run the cycle"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "moltbook", Args: map[string]any{"action": "list_notifications"}}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "1", Content: "ok"}}},
		{Role: "assistant", ToolCalls: []ToolCall{
			{Name: "moltbook", Args: map[string]any{"action": "upvote_post"}},
			{Name: "update_notes", Args: map[string]any{"section": "ENGAGED"}},
		}},
		{Role: "user", ToolResults: []ToolResult{{ID: "2", Content: "ok"}}},
		{Role: "assistant", Content: "the withheld report"},
	}
	at, req := lastUserRequestAt(history)
	if req != "run the cycle" {
		t.Fatalf("request = %q", req)
	}
	got := haltedTurnCalls(history, at, GuardHookPreOutput)
	want := []string{"moltbook/list_notifications", "moltbook/upvote_post", "update_notes"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pre_output: got %v, want %v", got, want)
	}
	// A pre_action halt's own round is the one the rule refused: not done.
	got = haltedTurnCalls(history[:7], at, GuardHookPreAction)
	if want := []string{"moltbook/list_notifications"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pre_action: got %v, want %v", got, want)
	}
}

// The loop hands the calls over when it halts on the reply.
func TestGuardrailRejectReceivesTheTurnsCalls(t *testing.T) {
	app, _ := withTierStubs(t, "test.reject.did", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{{ID: "c1", Name: "probe", Args: map[string]any{"action": "look"}}}
		}
		return nil
	})
	var got GuardrailHalt
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds: 4,
		RouteKey:  "test.reject.did",
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "probe", Description: "probe", Parameters: map[string]ToolParam{"action": {Type: "string"}}},
			Handler: func(context.Context, map[string]any) (string, error) { return "seen", nil },
		}},
		GuardrailCheck: func(hook, candidate string) GuardrailDecision {
			return GuardrailDecision{Blocked: hook == GuardHookPreOutput, Message: "blocked"}
		},
		GuardrailHalted: func() bool { return true },
		GuardrailReject: func(h GuardrailHalt) string { got = h; return "held back" },
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got.Reason != GuardHookPreOutput || got.Request != "go" || !reflect.DeepEqual(got.Did, []string{"probe/look"}) {
		t.Errorf("halt = %+v", got)
	}
}
