package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A model that drafts several variations and fires them all at one recipient in
// ONE round must deliver only the first; the rest are held (not sent) with a
// note. Keyed on recipient, so the varied text doesn't let them slip the guard.
func TestSendGuardHoldsBatchedDuplicateSends(t *testing.T) {
	app, _ := withTierStubs(t, "test.sendguard", func(n int) []ToolCall {
		if n == 1 {
			// Four "jokes" to the same group chat in one response.
			return []ToolCall{
				{ID: "1", Name: "message_contact", Args: map[string]any{"to": "Group Message", "text": "joke A"}},
				{ID: "2", Name: "message_contact", Args: map[string]any{"to": "Group Message", "text": "joke B"}},
				{ID: "3", Name: "message_contact", Args: map[string]any{"to": "Group Message", "text": "joke C"}},
				{ID: "4", Name: "message_contact", Args: map[string]any{"to": "Group Message", "text": "joke D"}},
			}
		}
		return nil // stop
	})

	var delivered int
	sendTool := AgentToolDef{
		Tool: Tool{Name: "message_contact", Description: "send", Parameters: map[string]ToolParam{
			"to": {Type: "string"}, "text": {Type: "string"},
		}},
		Handler: func(args map[string]any) (string, error) {
			delivered++
			return "sent", nil
		},
	}

	guardKey := func(name string, args map[string]any) string {
		if name == "message_contact" {
			if to, _ := args["to"].(string); to != "" {
				return "send:" + strings.ToLower(to)
			}
		}
		return ""
	}

	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "joke"}}, AgentLoopConfig{
		Tools:        []AgentToolDef{sendTool},
		MaxRounds:    4,
		RouteKey:     "test.sendguard",
		SendGuardKey: guardKey,
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("only the FIRST send to the recipient should be delivered; got %d deliveries", delivered)
	}
}

// A send to a DIFFERENT recipient is not held, and a second send to the same
// recipient in a LATER round is held too (cross-round, not just per-batch).
func TestSendGuardPerRecipientAndCrossRound(t *testing.T) {
	app, _ := withTierStubs(t, "test.sendguard2", func(n int) []ToolCall {
		switch n {
		case 1:
			return []ToolCall{
				{ID: "1", Name: "message_contact", Args: map[string]any{"to": "Alice", "text": "hi"}},
				{ID: "2", Name: "message_contact", Args: map[string]any{"to": "Bob", "text": "hi"}},
			}
		case 2:
			// Same recipient as round 1 → held.
			return []ToolCall{{ID: "3", Name: "message_contact", Args: map[string]any{"to": "Alice", "text": "again"}}}
		}
		return nil
	})
	seen := map[string]int{}
	sendTool := AgentToolDef{
		Tool: Tool{Name: "message_contact", Parameters: map[string]ToolParam{"to": {Type: "string"}, "text": {Type: "string"}}},
		Handler: func(args map[string]any) (string, error) {
			seen[fmt.Sprint(args["to"])]++
			return "sent", nil
		},
	}
	guardKey := func(name string, args map[string]any) string {
		if to, _ := args["to"].(string); name == "message_contact" && to != "" {
			return "send:" + strings.ToLower(to)
		}
		return ""
	}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools: []AgentToolDef{sendTool}, MaxRounds: 4, RouteKey: "test.sendguard2", SendGuardKey: guardKey,
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if seen["Alice"] != 1 {
		t.Errorf("Alice should get exactly one delivery (round-2 repeat held), got %d", seen["Alice"])
	}
	if seen["Bob"] != 1 {
		t.Errorf("Bob (distinct recipient) should be delivered, got %d", seen["Bob"])
	}
}
