package core

import (
	"context"
	"strings"
	"testing"
)

// TestWatchAlertDirectPostsVerbatim pins the "why am I seeing 'Watch monitor X
// detected a change / What changed / Current output' on a DIRECT channel post"
// fix. A no-LLM delivery (direct/text) with no format_script must post the
// tool's output verbatim; only an agent-wake (channel) gets the diff wrapper.
func TestWatchAlertDirectPostsVerbatim(t *testing.T) {
	const current = "SouthPawn has left TeamSpeak."
	ctx := context.Background()

	// direct=true → verbatim, no wrapper.
	got, suppress := formatWatchAlert(ctx, "u", "ts3-watch", "", "prior", current, true)
	if suppress {
		t.Fatal("direct delivery with content must not suppress")
	}
	if got != current {
		t.Fatalf("direct delivery must post verbatim; got %q want %q", got, current)
	}
	if strings.Contains(got, "detected a change") || strings.Contains(got, "Current output") {
		t.Fatalf("direct delivery must NOT include the diff wrapper; got %q", got)
	}

	// direct=false (agent wake) → keeps the diagnostic wrapper.
	wrapped, _ := formatWatchAlert(ctx, "u", "ts3-watch", "", "prior", current, false)
	if !strings.Contains(wrapped, "detected a change") {
		t.Fatalf("agent-wake delivery must keep the wrapper; got %q", wrapped)
	}

	// direct + empty current → suppress (don't post a blank line).
	if _, sup := formatWatchAlert(ctx, "u", "ts3-watch", "", "prior", "   ", true); !sup {
		t.Fatal("direct delivery with empty output must suppress, not post blank")
	}

	// An HTTP status prefix (api-mode tool) is stripped on the direct path.
	body, _ := formatWatchAlert(ctx, "u", "w", "", "", "HTTP 200 OK\nSouthPawn joined.", true)
	if body != "SouthPawn joined." {
		t.Fatalf("direct delivery must strip the HTTP status line; got %q", body)
	}
}
