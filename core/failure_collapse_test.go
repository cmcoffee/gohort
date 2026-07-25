package core

import (
	"strings"
	"testing"
)

func failMsg(id, content string) Message {
	return Message{Role: "user", ToolResults: []ToolResult{{ID: id, Content: content, IsError: true}}}
}

const collapseErr = "Error: url is required for mark_notifications_read dispatch"

// TestCollapseKeepsFirstAndRewritesMiddles pins the core damper contract:
// duplicate failure results collapse to the marker, the FIRST occurrence
// keeps its full text, and keepLast additionally preserves the newest copy.
func TestCollapseKeepsFirstAndRewritesMiddles(t *testing.T) {
	shape := normalizeFailureShape(collapseErr)
	if shape == "" {
		t.Fatal("fixture error must produce a shape")
	}
	hist := []Message{failMsg("a", collapseErr), failMsg("b", collapseErr), failMsg("c", collapseErr), failMsg("d", collapseErr)}

	// keepLast=false (live turn: the current round's copy is appended after
	// the sweep) — everything after the first collapses.
	n := collapseRepeatedFailureResults(hist, shape, false)
	if n != 3 {
		t.Fatalf("want 3 collapsed, got %d", n)
	}
	if hist[0].ToolResults[0].Content != collapseErr {
		t.Fatalf("first occurrence must keep full text; got %q", hist[0].ToolResults[0].Content)
	}
	for i := 1; i < 4; i++ {
		if hist[i].ToolResults[0].Content != collapsedFailureMarker(shape) {
			t.Fatalf("occurrence %d should be the marker; got %q", i, hist[i].ToolResults[0].Content)
		}
		if !hist[i].ToolResults[0].IsError {
			t.Fatalf("collapse must not flip IsError")
		}
	}
	// Idempotent: markers no longer match the shape, so a second sweep is a
	// no-op instead of a marker-of-marker cascade.
	if again := collapseRepeatedFailureResults(hist, shape, false); again != 0 {
		t.Fatalf("second sweep should collapse nothing, got %d", again)
	}
}

// TestCollapseIncomingKeepsNewest pins the loop-start variant: incoming
// history keeps the first AND the newest full copy (the newest is the
// current state), collapsing only the middle of the wall.
func TestCollapseIncomingKeepsNewest(t *testing.T) {
	hist := []Message{failMsg("a", collapseErr), failMsg("b", collapseErr), failMsg("c", collapseErr), failMsg("d", collapseErr)}
	if n := collapseIncomingFailureStreaks(hist); n != 2 {
		t.Fatalf("want 2 collapsed (middle two), got %d", n)
	}
	if hist[0].ToolResults[0].Content != collapseErr || hist[3].ToolResults[0].Content != collapseErr {
		t.Fatal("first and newest occurrences must keep full text")
	}
	marker := collapsedFailureMarker(normalizeFailureShape(collapseErr))
	if hist[1].ToolResults[0].Content != marker || hist[2].ToolResults[0].Content != marker {
		t.Fatal("middle occurrences should be markers")
	}
	// Below the threshold nothing collapses — five distinct failures are five
	// facts, not a streak.
	distinct := []Message{failMsg("a", "Error: connection refused to host alpha-system"), failMsg("b", "Error: certificate expired on beta-service endpoint")}
	if n := collapseIncomingFailureStreaks(distinct); n != 0 {
		t.Fatalf("distinct failures must not collapse; got %d", n)
	}
}

// TestCollapseClonesSharedBackingArrays pins the copy-on-write contract:
// RunAgentLoop's history is a shallow copy of the caller's messages, so the
// rewrite must never reach the caller's ToolResults.
func TestCollapseClonesSharedBackingArrays(t *testing.T) {
	caller := []Message{failMsg("a", collapseErr), failMsg("b", collapseErr), failMsg("c", collapseErr)}
	hist := make([]Message, len(caller))
	copy(hist, caller) // shallow — ToolResults backing arrays shared
	if n := collapseRepeatedFailureResults(hist, normalizeFailureShape(collapseErr), false); n != 2 {
		t.Fatalf("want 2 collapsed, got %d", n)
	}
	for i, m := range caller {
		if m.ToolResults[0].Content != collapseErr {
			t.Fatalf("caller's message %d was mutated through the shared backing array: %q", i, m.ToolResults[0].Content)
		}
	}
}

// TestRetireResolvedFailureResults pins the success half: once the tool
// works, ALL its prior failure results (first included) become resolved
// markers naming the tool.
func TestRetireResolvedFailureResults(t *testing.T) {
	hist := []Message{failMsg("a", collapseErr), failMsg("b", collapseErr)}
	shapes := map[string]bool{normalizeFailureShape(collapseErr): true}
	if n := retireResolvedFailureResults(hist, shapes, "mark_notifications_read"); n != 2 {
		t.Fatalf("want 2 retired, got %d", n)
	}
	for i := range hist {
		got := hist[i].ToolResults[0].Content
		if !strings.Contains(got, "SUCCEEDED") || !strings.Contains(got, "mark_notifications_read") {
			t.Fatalf("occurrence %d should be a resolved marker naming the tool; got %q", i, got)
		}
	}
	// A failure shape from a DIFFERENT tool stays untouched.
	other := []Message{failMsg("x", "Error: connection refused to host alpha-system")}
	if n := retireResolvedFailureResults(other, shapes, "mark_notifications_read"); n != 0 {
		t.Fatalf("unrelated failure must not be retired; got %d", n)
	}
}
