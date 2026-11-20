package llmcalls

import (
	"context"
	"testing"
)

// Calls are kept only for a context watched, and said in words: how each
// stopped, what it wrote and thought, numbered when there are several.
func TestCallsAreKeptForAWatcherAndSaid(t *testing.T) {
	Note(context.Background(), Call{Output: 1})
	Note(nil, Call{Output: 1})
	ctx, calls := Watch(context.Background())
	if len(calls()) != 0 || Describe(nil) != "" {
		t.Fatal("a fresh watch has calls")
	}
	Note(ctx, Call{Tier: "worker", Stop: "max_tokens", Input: 10, Output: 600, Reasoning: 580})
	if got := Describe(calls()); got != "stopped at its output limit after 600 output tokens, about 580 of them thinking, 10 in (worker)" {
		t.Errorf("one call: %q", got)
	}
	Note(ctx, Call{Stop: "end_turn", Output: 5})
	if got := Describe(calls()); got != "2 calls: 1) stopped at its output limit after 600 output tokens, about 580 of them thinking, 10 in (worker); 2) finished after 5 output tokens, 0 in" {
		t.Errorf("two calls: %q", got)
	}
	if got := describeOne(Call{Stop: "refusal", Detail: "Bedrock said content_filtered", Output: 44}); got != "declined (Bedrock said content_filtered) after 44 output tokens, 0 in" {
		t.Errorf("a provider's own stop: %q", got)
	}
	for stop, want := range map[string]string{
		"length": "stopped at its output limit", "tool_use": "stopped to call a tool", "refusal": "declined",
		"": "ended (no stop reason given)", "pause_turn": "ended (pause_turn)",
	} {
		if got := describeOne(Call{Stop: stop}); got[:len(want)] != want {
			t.Errorf("stop %q: %q", stop, got)
		}
	}
}

// Watched is true only under a watch.
func TestWatchedIsOnlyUnderAWatch(t *testing.T) {
	w, _ := Watch(context.Background())
	if !Watched(w) || Watched(context.Background()) || Watched(nil) {
		t.Error("Watched")
	}
}
