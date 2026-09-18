package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The live chat path used to scrub framework markers in the BROWSER only: the
// channel runner, the export, the worker and the task runner all called
// StripMetaTags, and the direct reply never did. The rendered bubble looked
// clean while the stored message kept the marker, so anything reading the row
// back (a copy, a bridge, an export of the raw content) got it verbatim.
func TestCleanBubbleTextScrubsBothMarkupKinds(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"framework marker", "answer <gohort-meta>internal note</gohort-meta> here", "answer  here"},
		{"tool-call markup typed as prose", "before <tool_call>{\"name\":\"x\"}</tool_call> after", "before  after"},
		{"both at once", "<gohort-meta>plan</gohort-meta>ok <tool_call>{}</tool_call>", "ok"},
		// A reply cut at the output limit settles as its own bubble, so the
		// opener can be the last thing in it and the closer arrives in the
		// continuation. The half that settles first must not ship the marker.
		{"unterminated marker", "partial answer <gohort-meta>note that never closes", "partial answer"},
		{"ordinary reply untouched", "just a normal answer", "just a normal answer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanBubbleText(tc.in); got != tc.want {
				t.Errorf("cleanBubbleText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// captureMidTurnBubble is the one door narration takes into the transcript, so
// the scrub sits there rather than at each caller — the restored lead-in path
// hands over text that never passed through cleanBubbleText.
func TestCaptureMidTurnBubbleScrubsMarkers(t *testing.T) {
	var turn chatTurn
	turn.captureMidTurnBubble("narration <gohort-meta>internal</gohort-meta> continues")
	turn.captureMidTurnBubble("<gohort-meta>nothing but a note</gohort-meta>")

	bubbles := turn.drainMidTurnBubbles()
	if len(bubbles) != 1 {
		t.Fatalf("got %d captured bubbles, want 1 (a marker-only bubble is empty once scrubbed)", len(bubbles))
	}
	if strings.Contains(bubbles[0].Content, "gohort-meta") {
		t.Errorf("marker persisted into the transcript: %q", bubbles[0].Content)
	}
	if want := "narration  continues"; bubbles[0].Content != want {
		t.Errorf("captured %q, want %q", bubbles[0].Content, want)
	}
}

// The saved copy also gets the house-style enforcers, which every other
// persisting surface (channels, export, the synthesis path, the task runner)
// already applied and the live chat path did not. The same reply used to be
// stored one way through the plan path and another way direct, and the direct
// one only LOOKED right because the browser re-stripped em-dashes at render.
func TestCaptureMidTurnBubbleAppliesHouseStyle(t *testing.T) {
	var turn chatTurn
	turn.captureMidTurnBubble("I'm around\u2014otherwise, sleep well")

	bubbles := turn.drainMidTurnBubbles()
	if len(bubbles) != 1 {
		t.Fatalf("got %d captured bubbles, want 1", len(bubbles))
	}
	if strings.ContainsRune(bubbles[0].Content, '\u2014') {
		t.Errorf("em-dash survived into the transcript: %q", bubbles[0].Content)
	}
	if want := "I'm around, otherwise, sleep well"; bubbles[0].Content != want {
		t.Errorf("captured %q, want %q", bubbles[0].Content, want)
	}
}

// A diagnostic is text a person reads, so it leaves through the same delivery
// scrub as a reply. The framework wrote em-dashes into a dozen of its own diag
// strings, and a detail routinely quotes the model, which can carry a marker.
func TestTurnDiagAppliesTheDeliveryScrub(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	buf := &syncBuf{}
	turn := &chatTurn{
		agent:   AgentRecord{ID: "lead", Name: "Lead"},
		udb:     udb,
		ctx:     context.Background(),
		sse:     &sseWriter{live: buf},
		session: &ChatSession{ID: "conv-1", AgentID: "lead"},
	}
	turn.turnDiag("guardrail-blocked",
		"Guardrail check could not run—BLOCKED. <gohort-meta>internal</gohort-meta>")

	trail := decorateSessionDiags(parentTrailOf(udb, "lead", "conv-1"))
	if len(trail) != 1 {
		t.Fatalf("trail = %+v", trail)
	}
	if strings.ContainsRune(trail[0].Detail, '—') {
		t.Errorf("em-dash reached the trail: %q", trail[0].Detail)
	}
	if strings.Contains(trail[0].Detail, "gohort-meta") {
		t.Errorf("marker reached the trail: %q", trail[0].Detail)
	}
	if want := "Guardrail check could not run, BLOCKED."; trail[0].Detail != want {
		t.Errorf("detail = %q, want %q", trail[0].Detail, want)
	}
}
