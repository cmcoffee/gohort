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
	ctx := WithPromptTurn(context.Background(), "", "turn-stable")
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
	ctx := WithPromptTurn(context.Background(), "", "turn-tools")
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
	ctx := WithPromptTurn(context.Background(), "", "turn-system")
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
	a := WithPromptTurn(context.Background(), "", "turn-a")
	b := WithPromptTurn(context.Background(), "", "turn-b")
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

// A new conversation is compared with its family's last call, and the line
// says which way it went: the same prefix, or what moved first.
func TestWatchComparesANewConversationWithTheLast(t *testing.T) {
	tools := []Tool{{Name: "search", Description: "Search the web."}, {Name: "read", Description: "Read a page."}}
	first := func(family, conv string, system string, tools []Tool) string {
		lines := captureLog(t)
		WatchPromptPrefix(WithPromptTurn(context.Background(), family, conv), system, tools)
		return strings.Join(*lines, "\n")
	}
	first("agent=w", "w-1", "SYSTEM", tools)

	if got := first("agent=w", "w-2", "SYSTEM", tools); !strings.Contains(got, "same system prompt and tools") {
		t.Errorf("an unchanged prefix must say so, pointing at the server's cache: %q", got)
	}
	more := append(append([]Tool{}, tools...), Tool{Name: "from_client_files"})
	if got := first("agent=w", "w-3", "SYSTEM", more); !strings.Contains(got, "TOOLS CHANGED (2 → 3 +from_client_files)") {
		t.Errorf("an added tool must be named: %q", got)
	}
	reworded := []Tool{{Name: "search", Description: "Search the web. Now at 07:01."}, {Name: "read", Description: "Read a page."}, {Name: "from_client_files"}}
	if got := first("agent=w", "w-4", "SYSTEM", reworded); !strings.Contains(got, "TOOL SCHEMAS CHANGED search") || strings.Contains(got, "read") {
		t.Errorf("a reworded tool, and only it, must be named: %q", got)
	}
	if got := first("agent=w", "w-5", "SYSTEM, now longer", reworded); !strings.Contains(got, "SYSTEM PROMPT CHANGED at byte 6 of 18") {
		t.Errorf("a changed system prompt must be located: %q", got)
	}
	swapped := []Tool{reworded[1], reworded[0], reworded[2]}
	if got := first("agent=w", "w-6", "SYSTEM, now longer", swapped); !strings.Contains(got, "TOOLS REORDERED") {
		t.Errorf("a reordered catalog must say so: %q", got)
	}
	// Another family, and an unlabelled one, are not compared with this one.
	if got := first("agent=other", "o-1", "ELSEWHERE", nil); got != "" {
		t.Errorf("a family's first conversation has nothing to compare with: %q", got)
	}
	if got := first("", "x-1", "ELSEWHERE", nil); got != "" {
		t.Errorf("a turn with no family is compared within itself only: %q", got)
	}
}

// Later calls of a conversation are compared within it, as before, and still
// keep the family's last call current.
func TestWatchKeepsTheFamilyCurrent(t *testing.T) {
	lines := captureLog(t)
	a := WithPromptTurn(context.Background(), "agent=k", "k-1")
	WatchPromptPrefix(a, "ONE", nil)
	WatchPromptPrefix(a, "TWO", nil)
	*lines = nil
	WatchPromptPrefix(WithPromptTurn(context.Background(), "agent=k", "k-2"), "TWO", nil)
	if got := strings.Join(*lines, "\n"); !strings.Contains(got, "same system prompt") {
		t.Errorf("the next conversation must be compared with the latest call, not the first: %q", got)
	}
}
