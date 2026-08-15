package core

// The watch exists to turn "the cache is being invalidated somehow" into
// a name, a byte offset, and a cost. Its value is entirely in what the
// line SAYS, so that is what these test.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func captureLog(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	prev := Log
	Log = func(v ...any) {
		if len(v) > 0 {
			if f, ok := v[0].(string); ok {
				lines = append(lines, fmt.Sprintf(f, v[1:]...))
				return
			}
		}
	}
	t.Cleanup(func() { Log = prev })
	return &lines
}

func TestWatchIsSilentWhenThePrefixHolds(t *testing.T) {
	lines := captureLog(t)
	ctx := WithPromptTurn(context.Background(), "turn-stable")
	tools := []Tool{{Name: "a"}, {Name: "b"}}
	for i := 0; i < 5; i++ {
		WatchPromptPrefix(ctx, "SYSTEM PROMPT", tools)
	}
	if len(*lines) != 0 {
		t.Errorf("a stable prefix should say nothing — that is the healthy case:\n%v", *lines)
	}
}

func TestWatchNamesAToolCatalogChange(t *testing.T) {
	lines := captureLog(t)
	ctx := WithPromptTurn(context.Background(), "turn-tools")
	WatchPromptPrefix(ctx, "SYS", []Tool{{Name: "search"}, {Name: "read"}})
	WatchPromptPrefix(ctx, "SYS", []Tool{{Name: "search"}, {Name: "read"}, {Name: "load_tool_x"}})

	if len(*lines) != 1 {
		t.Fatalf("expected exactly one line: %v", *lines)
	}
	got := (*lines)[0]
	// The name of the arrival is the whole point — it is what somebody
	// greps for to find the code that added it.
	if !strings.Contains(got, "+load_tool_x") {
		t.Errorf("the line must name what arrived: %s", got)
	}
	// And it must say why a tool change is the expensive one.
	if !strings.Contains(got, "entire conversation are re-written") {
		t.Errorf("the line should say what it costs: %s", got)
	}
}

func TestWatchLocatesASystemPromptChange(t *testing.T) {
	lines := captureLog(t)
	ctx := WithPromptTurn(context.Background(), "turn-system")
	base := "You are helpful.\nPHASE: triage\n" + strings.Repeat("x", 2000)
	moved := "You are helpful.\nPHASE: verify\n" + strings.Repeat("x", 2000)
	WatchPromptPrefix(ctx, base, nil)
	WatchPromptPrefix(ctx, moved, nil)

	if len(*lines) != 1 {
		t.Fatalf("expected one line: %v", *lines)
	}
	got := (*lines)[0]
	// The byte offset is the payload: it says how much of the prefix
	// survived, and the snippet says which block moved.
	if !strings.Contains(got, "byte 24") {
		t.Errorf("should locate the divergence precisely: %s", got)
	}
	if !strings.Contains(got, "triage") || !strings.Contains(got, "verify") {
		t.Errorf("should show what it was and what it became: %s", got)
	}
	if !strings.Contains(got, "re-written this call") {
		t.Errorf("should state the consequence: %s", got)
	}
}

// Two turns running at once must not read as one turn changing.
func TestWatchKeepsTurnsApart(t *testing.T) {
	lines := captureLog(t)
	a := WithPromptTurn(context.Background(), "turn-a")
	b := WithPromptTurn(context.Background(), "turn-b")
	WatchPromptPrefix(a, "SYSTEM A", []Tool{{Name: "x"}})
	WatchPromptPrefix(b, "SYSTEM B", []Tool{{Name: "y"}})
	WatchPromptPrefix(a, "SYSTEM A", []Tool{{Name: "x"}})
	WatchPromptPrefix(b, "SYSTEM B", []Tool{{Name: "y"}})
	if len(*lines) != 0 {
		t.Errorf("interleaved turns were compared against each other:\n%v", *lines)
	}
}

// An unlabelled caller pays nothing and hears nothing.
func TestWatchIsOffWithoutALabel(t *testing.T) {
	lines := captureLog(t)
	WatchPromptPrefix(context.Background(), "A", []Tool{{Name: "x"}})
	WatchPromptPrefix(context.Background(), "B", []Tool{{Name: "y"}})
	if len(*lines) != 0 {
		t.Errorf("the watch fired for a caller that never opted in:\n%v", *lines)
	}
}
