package core

import "testing"

// TestLatestUserContent: returns the most recent user turn.
func TestLatestUserContent(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	if got := LatestUserContent(msgs); got != "second" {
		t.Errorf("expected latest user content 'second', got %q", got)
	}
	if got := LatestUserContent([]Message{{Role: "assistant", Content: "x"}}); got != "" {
		t.Errorf("no user message should yield empty, got %q", got)
	}
}
