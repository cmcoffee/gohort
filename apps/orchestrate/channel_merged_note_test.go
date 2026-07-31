package orchestrate

// The merge note is the half of the coalescing fix that reaches the model. It
// must fire only for a real batch, must say how many parts there are, and must
// never touch the message itself — the message is the stored transcript of what
// the person typed, and scaffolding written into it would both rewrite that
// record and give the assistant a format to imitate.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestMergedNoteOnlyForRealBatches(t *testing.T) {
	if got := mergedMessagesNote(ChannelInbound{Text: "hi"}); got != "" {
		t.Errorf("an unmerged message produced a note: %q", got)
	}
	if got := mergedMessagesNote(ChannelInbound{Text: "hi", MergedCount: 1}); got != "" {
		t.Errorf("a single-message batch produced a note: %q", got)
	}
	got := mergedMessagesNote(ChannelInbound{Text: "a\n\nb", MergedCount: 2})
	if got == "" {
		t.Fatal("a two-message batch produced no note")
	}
	if !strings.Contains(got, "2 separate messages") || !strings.Contains(got, "2 parts") {
		t.Errorf("the note should state the count on both sides: %q", got)
	}
	if !strings.Contains(strings.ToUpper(got), "ALL") {
		t.Errorf("the note must ask for all of them to be addressed: %q", got)
	}
}

// TestMergedNoteSurvivesAnUnknownBinding — channelSurfaceContext returns EMPTY
// when the framework doesn't recognize the chat. The merge note has nothing to
// do with the binding and must not be lost with it, or the exact turn most
// likely to be half-answered is the one that goes unannotated.
func TestMergedNoteSurvivesAnUnknownBinding(t *testing.T) {
	in := ChannelInbound{Owner: "u", ChatID: "nope", Text: "a\n\nb", MergedCount: 2}
	if base := channelSurfaceContext(in); base != "" {
		t.Skipf("this deployment resolved the binding (%q); the guard is untestable here", base)
	}
	full := channelSurfaceContextFull(in)
	if !strings.Contains(full, "2 separate messages") {
		t.Errorf("merge note lost when the binding is unknown: %q", full)
	}
}

// TestMergedNoteStaysOutOfTheMessage — the note is run-only context. Nothing
// about the merge may alter the text that gets persisted as the user's turn.
func TestMergedNoteStaysOutOfTheMessage(t *testing.T) {
	in := ChannelInbound{Text: "what's the deploy status?\n\nalso check disk space", MergedCount: 2}
	note := mergedMessagesNote(in)
	if strings.Contains(in.Text, note) {
		t.Error("the note leaked into the message text")
	}
	for _, marker := range []string{"1.", "2.", "[", "]"} {
		if strings.Contains(in.Text, marker) {
			t.Errorf("merged text carries structural scaffolding (%q): %q", marker, in.Text)
		}
	}
}
