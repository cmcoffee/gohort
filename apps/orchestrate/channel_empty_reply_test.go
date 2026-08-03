// Why a channel sent the generic "I wasn't able to put together a response".
// The message asks the contact to rephrase, which is misleading in every case
// that produces it — the request was understood; the agent said nothing back.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestSilenceIsDeliberateAndEmptinessIsNot(t *testing.T) {
	// stay_silent means the model was told not to reply and complied. Talking
	// over that with a fallback answers "don't reply to this" with "I wasn't
	// able to put together a response".
	if _, deliver := channelDelivery("", nil, nil, true); deliver {
		t.Error("a deliberate silence must deliver nothing at all")
	}
	// Empty by accident is a failure: the contact texted and expects something.
	text, deliver := channelDelivery("", nil, nil, false)
	if !deliver || text != channelEmptyFallback {
		t.Errorf("accidental emptiness must fall back, got %q deliver=%v", text, deliver)
	}
}

func TestAnAttachmentAloneIsAReply(t *testing.T) {
	// "Here, look at this" with no caption is a real answer — and stay_silent
	// paired with a produced file is a documented shape (deliver the file, say
	// nothing), so an attachment outranks both emptiness rules.
	if text, deliver := channelDelivery("", []string{"PICTURE"}, nil, false); !deliver || text != "" {
		t.Errorf("an image with no caption must go out as-is, got %q deliver=%v", text, deliver)
	}
	if _, deliver := channelDelivery("", nil, []string{"CLIP"}, true); !deliver {
		t.Error("a produced file must deliver even when the model chose silence")
	}
}

func TestTheFallbackIsNotSubstitutedForRealText(t *testing.T) {
	if text, deliver := channelDelivery("here you go", nil, nil, false); !deliver || text != "here you go" {
		t.Errorf("real text must pass through untouched, got %q", text)
	}
	if strings.Contains(channelEmptyFallback, "error") {
		t.Error("the fallback should read as a person, not a stack trace")
	}
}

var _ = ToolSession{}
