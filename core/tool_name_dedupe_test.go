package core

// A tool name reaches the model once per request. A kit assembled from two
// sources (a writer's references and the turn's) sent names twice: some
// providers refuse the whole request for that, and the handler map kept the
// LAST definition while the model read the first.

import (
	"context"
	"testing"
)

func TestAToolNameIsOfferedOnceAndRunsTheFirstDefinition(t *testing.T) {
	ran := ""
	kit := []AgentToolDef{
		{Tool: Tool{Name: "search", Description: "first"}, Handler: func(context.Context, map[string]any) (string, error) { ran = "first"; return "ok", nil }},
		{Tool: Tool{Name: "facts"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }},
		{Tool: Tool{Name: "search", Description: "second"}, Handler: func(context.Context, map[string]any) (string, error) { ran = "second"; return "ok", nil }},
	}
	stub := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
		{Content: "done"},
	}}
	if _, err := chatToolLoop(context.Background(), stub.Chat, []Message{{Role: "user", Content: "go"}}, kit, 4); err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, tl := range stub.Config(0).Tools {
		names[tl.Name]++
	}
	if names["search"] != 1 || names["facts"] != 1 {
		t.Errorf("a tool name was offered more than once: %v", names)
	}
	if ran != "first" {
		t.Errorf("the model was shown the first definition but %q ran", ran)
	}
}

// Every request with tools goes through WithTools, so it holds the rule for
// paths that assemble their own list.
func TestWithToolsNeverSendsANameTwice(t *testing.T) {
	var c ChatConfig
	WithTools([]Tool{{Name: "a", Description: "one"}, {Name: "b"}, {Name: "a", Description: "two"}})(&c)
	if len(c.Tools) != 2 || c.Tools[0].Description != "one" {
		t.Errorf("want [a(one) b], got %+v", c.Tools)
	}
}
