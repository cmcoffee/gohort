package core

// Merging is where a coalesced batch stops being several messages and becomes
// one, and it is lossy in exactly one way that matters: two asks arrive as a
// single user turn with nothing marking them as two. The model answers the
// salient one and drops the rest, which is why the same path sometimes returned
// a complete answer and sometimes half of one.
//
// These pin what the merge preserves: the count, and a shape the text itself is
// honest about.

import (
	"strings"
	"testing"
)

func TestMergeCountsOriginalMessages(t *testing.T) {
	a := ChannelInbound{Text: "what's the deploy status?"}
	b := ChannelInbound{Text: "also check disk space"}
	c := ChannelInbound{Text: "and the cert expiry"}

	two := mergeInbound(a, b)
	if two.MergedCount != 2 {
		t.Errorf("merging two singles = %d, want 2", two.MergedCount)
	}
	three := mergeInbound(two, c)
	if three.MergedCount != 3 {
		t.Errorf("merging a pair with a single = %d, want 3", three.MergedCount)
	}
	// An unmerged inbound stands for itself, so a batch of one is never
	// mistaken for a batch of none.
	if messageCount(a) != 1 {
		t.Errorf("a lone message counts as %d, want 1", messageCount(a))
	}
}

func TestMergeSeparatesBubblesWithABlankLine(t *testing.T) {
	got := mergeInbound(
		ChannelInbound{Text: "what's the deploy status?"},
		ChannelInbound{Text: "also check disk space"},
	)
	if !strings.Contains(got.Text, "status?\n\nalso") {
		t.Errorf("bubbles should be separated by a blank line, got %q", got.Text)
	}
	// Both survive, in order, unaltered — this is the person's own text and
	// the stored transcript of what they typed.
	if !strings.HasPrefix(got.Text, "what's the deploy status?") || !strings.HasSuffix(got.Text, "also check disk space") {
		t.Errorf("merge reordered or rewrote the text: %q", got.Text)
	}
}

func TestMergeWithEmptyTextDoesNotLeadWithBlankLines(t *testing.T) {
	// A bare image with no caption, then a question. The empty side must not
	// contribute separator whitespace.
	got := mergeInbound(
		ChannelInbound{Images: []string{"img"}},
		ChannelInbound{Text: "what is this?"},
	)
	if got.Text != "what is this?" {
		t.Errorf("empty base contributed separators: %q", got.Text)
	}
	if got.MergedCount != 2 {
		t.Errorf("an image plus a question is still two messages, got %d", got.MergedCount)
	}
	if len(got.Images) != 1 {
		t.Errorf("attachments lost in the merge: %+v", got.Images)
	}
}

func TestSingleMessageCarriesNoMergeState(t *testing.T) {
	// The overwhelmingly common case: one message, never merged. It must look
	// exactly as it did before any of this existed.
	in := ChannelInbound{Text: "hello"}
	if in.MergedCount != 0 {
		t.Errorf("an unmerged inbound should carry no count, got %d", in.MergedCount)
	}
}
