package core

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
)

// A handler can name the call it is running: a call that runs others beneath
// it hands this id down as their parent. Alone in a round or beside others.
func TestAHandlerKnowsWhichCallItIs(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	probe := AgentToolDef{
		Tool: Tool{Name: "probe", Description: "d", Parameters: map[string]ToolParam{"n": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, args["n"].(string)+"="+ToolCallIDFrom(ctx))
			return "ok", nil
		},
	}
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "probe", Args: map[string]any{"n": "alone"}}}},
		{ToolCalls: []ToolCall{
			{ID: "c2", Name: "probe", Args: map[string]any{"n": "first"}},
			{ID: "c3", Name: "probe", Args: map[string]any{"n": "second"}}}},
		{Content: "done", Repeat: true},
	}}}
	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 4}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	sort.Strings(seen)
	if got := strings.Join(seen, " "); got != "alone=c1 first=c2 second=c3" {
		t.Errorf("handlers saw %q", got)
	}

	// A model that writes its call as a tag in its reply: the loop names it.
	seen = nil
	tagged := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{Content: `<tool_call>{"name": "probe", "arguments": {"n": "tagged"}}</tool_call>`},
		{Content: "done", Repeat: true},
	}}}
	if _, _, err := tagged.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}},
		AgentLoopConfig{Tools: []AgentToolDef{probe}, MaxRounds: 3, PromptTools: true}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "tagged=prompt_") {
		t.Errorf("a tagged call's handler saw %q", seen)
	}
	if ToolCallIDFrom(context.Background()) != "" || ToolCallIDFrom(nil) != "" {
		t.Error("a context with no call running should name none")
	}
}
