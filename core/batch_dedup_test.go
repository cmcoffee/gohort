package core

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// A model that emits the SAME call (name + args) several times in one response
// must have it executed ONCE. The repeatFail/repeatSame counters can't catch
// this — both are updated after the round, so every sibling reads the same
// stale value and all of them pass. Live cost of the gap: a verifier action
// that really runs the tool hit the network three times for one question.
func TestBatchDedupRunsIdenticalCallOnce(t *testing.T) {
	app, _ := withTierStubs(t, "test.batchdedup", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "fetch_news", Args: map[string]any{"category": "all", "max_items": 5}},
				{ID: "2", Name: "fetch_news", Args: map[string]any{"category": "all", "max_items": 5}},
				{ID: "3", Name: "fetch_news", Args: map[string]any{"category": "all", "max_items": 5}},
			}
		}
		return nil
	})

	var ran int32
	tool := AgentToolDef{
		Tool: Tool{Name: "fetch_news", Description: "fetch", Parameters: map[string]ToolParam{
			"category": {Type: "string"}, "max_items": {Type: "integer"},
		}},
		Handler: func(args map[string]any) (string, error) {
			atomic.AddInt32(&ran, 1)
			return "HEADLINES", nil
		},
	}

	_, history, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "news"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 4, RouteKey: "test.batchdedup",
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if ran != 1 {
		t.Fatalf("identical batched calls should dispatch ONCE; handler ran %d times", ran)
	}

	// Every call id still needs a result, and the copies must carry the real
	// content — a model that gets an error for its own duplicate tends to
	// "fix" it by re-calling rather than by reading what it already has.
	var toolResults []ToolResult
	for _, m := range history {
		toolResults = append(toolResults, m.ToolResults...)
	}
	if len(toolResults) != 3 {
		t.Fatalf("each of the 3 tool_call ids needs a result; got %d", len(toolResults))
	}
	dupes := 0
	for _, r := range toolResults {
		if !strings.Contains(r.Content, "HEADLINES") {
			t.Errorf("result %q lost the canonical content", r.ID)
		}
		if r.IsError {
			t.Errorf("result %q marked as error; the canonical call succeeded", r.ID)
		}
		if strings.Contains(r.Content, "DUPLICATE CALL") {
			dupes++
		}
	}
	if dupes != 2 {
		t.Errorf("expected the 2 copies to be labelled as duplicates, got %d", dupes)
	}
}

// Dedup keys on the full signature, so calls that differ in ANY argument are
// distinct work and all of them run. Guarding this because the failure mode is
// silent: over-broad dedup would swallow real calls and look like a model that
// simply didn't ask.
func TestBatchDedupKeepsDistinctArgs(t *testing.T) {
	app, _ := withTierStubs(t, "test.batchdedup2", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "read_page", Args: map[string]any{"page": 1}},
				{ID: "2", Name: "read_page", Args: map[string]any{"page": 2}},
				{ID: "3", Name: "read_page", Args: map[string]any{"page": 1}},
			}
		}
		return nil
	})

	var ran int32
	tool := AgentToolDef{
		Tool:    Tool{Name: "read_page", Description: "read", Parameters: map[string]ToolParam{"page": {Type: "integer"}}},
		Handler: func(args map[string]any) (string, error) { atomic.AddInt32(&ran, 1); return "page body", nil },
	}

	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "read"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 4, RouteKey: "test.batchdedup2",
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if ran != 2 {
		t.Fatalf("page 1 and page 2 are distinct calls (only the repeat of page 1 collapses); handler ran %d times, want 2", ran)
	}
}
