package orchestrate

import "testing"

// Telling a channel bot "don't reply to this" answered back "I wasn't able to
// put together a response to that." The model obeyed the instruction —
// stay_silent blanks the reply by design — and the channel's never-go-silent
// fallback then talked over both the user and the model, because a deliberate
// silence and a failed turn look identical at that seam: empty text.

func TestChannelDeliversDeliberateSilenceAsNothing(t *testing.T) {
	text, deliver := channelDelivery("", nil, nil, true)
	if deliver {
		t.Errorf("stay_silent must deliver NOTHING, got %q", text)
	}
}

func TestChannelStillCoversAnAccidentalEmptyTurn(t *testing.T) {
	text, deliver := channelDelivery("", nil, nil, false)
	if !deliver {
		t.Fatal("an accidental empty turn must still say something — the contact started this")
	}
	if text != channelEmptyFallback {
		t.Errorf("expected the fallback, got %q", text)
	}
	// Whitespace-only is empty too — a stripped marker leaves exactly that.
	if _, deliver := channelDelivery("   \n ", nil, nil, false); !deliver {
		t.Error("whitespace-only output is an empty turn")
	}
}

// stay_silent PLUS an attachment is the tool's documented purpose: deliver the
// file with no caption. Silence must not swallow the thing the turn produced.
func TestChannelSilenceStillDeliversAttachments(t *testing.T) {
	for _, tc := range []struct {
		name           string
		images, videos []string
	}{
		{"image", []string{"b64img"}, nil},
		{"video", nil, []string{"b64vid"}},
	} {
		text, deliver := channelDelivery("", tc.images, tc.videos, true)
		if !deliver {
			t.Errorf("%s: a silenced turn with an attachment must still deliver", tc.name)
		}
		if text != "" {
			t.Errorf("%s: the caption should stay empty, got %q", tc.name, text)
		}
	}
}

// Real text is delivered untouched whatever the flag says — a model that
// produced a reply AND called stay_silent still said something, and the
// fallback must never replace genuine content.
func TestChannelPassesRealTextThrough(t *testing.T) {
	for _, silenced := range []bool{false, true} {
		text, deliver := channelDelivery("on my way", nil, nil, silenced)
		if !deliver || text != "on my way" {
			t.Errorf("silenced=%v: real text must pass through, got %q deliver=%v", silenced, text, deliver)
		}
	}
}
