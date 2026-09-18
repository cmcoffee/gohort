package core

// A watch polls by invoking one captured tool, and on a direct fire that is the
// ONLY thing that runs — no LLM turn follows it. The card said what changed and
// nothing about what was asked, on the one surface where nobody was watching.

import (
	"context"
	"strings"
	"testing"
)

func TestTheCheckRidesTheFireAsAToolStep(t *testing.T) {
	m := EventMonitor{
		Name: "price watch", Owner: "alice",
		ToolName: "fetch_url",
		ToolArgs: map[string]any{"url": "https://example.test/price"},
	}
	steps := WatchStepsFromContext(withWatchStep(context.Background(), m, `{"price":42}`, nil))
	if len(steps) != 1 {
		t.Fatalf("the check must ride the fire: %+v", steps)
	}
	if steps[0].Name != "fetch_url" {
		t.Errorf("name = %q", steps[0].Name)
	}
	if !strings.Contains(steps[0].Args, "example.test/price") {
		t.Errorf("the args say WHAT was asked: %q", steps[0].Args)
	}
	if !strings.Contains(steps[0].Result, "42") {
		t.Errorf("the result says what came back: %q", steps[0].Result)
	}
	if steps[0].Err != "" {
		t.Errorf("a successful check carries no error: %q", steps[0].Err)
	}
}

// A poll with no captured tool (http_poll, a plain poll) has no check to show,
// and must not manufacture an empty one.
func TestAPollWithNoCapturedToolCarriesNothing(t *testing.T) {
	if steps := WatchStepsFromContext(withWatchStep(context.Background(),
		EventMonitor{Name: "n", Owner: "o"}, "body", nil)); steps != nil {
		t.Errorf("no tool, no step: %+v", steps)
	}
	if steps := WatchStepsFromContext(context.Background()); steps != nil {
		t.Errorf("an unstamped context carries none: %+v", steps)
	}
	if steps := WatchStepsFromContext(nil); steps != nil {
		t.Errorf("a nil context is not a panic: %+v", steps)
	}
}

// The body of a watch is routinely a whole API response, and the card is a
// trace rather than a copy of it — the card's own text already says what
// CHANGED.
func TestTheCheckResultIsBounded(t *testing.T) {
	m := EventMonitor{Name: "n", Owner: "o", ToolName: "fetch_url"}
	steps := WatchStepsFromContext(withWatchStep(context.Background(), m, strings.Repeat("x", 5000), nil))
	if n := len([]rune(steps[0].Result)); n > 210 {
		t.Errorf("result not bounded: %d chars", n)
	}
}

// A failed check is part of the record — a monitor that stopped working is
// exactly what the owner needs to see.
func TestAFailedCheckSaysSo(t *testing.T) {
	m := EventMonitor{Name: "n", Owner: "o", ToolName: "fetch_url"}
	steps := WatchStepsFromContext(withWatchStep(context.Background(), m, "", context.DeadlineExceeded))
	if len(steps) != 1 || steps[0].Err == "" {
		t.Fatalf("the failure must carry: %+v", steps)
	}
	if steps[0].Result != "" {
		t.Errorf("a failed check returned no body: %q", steps[0].Result)
	}
}
