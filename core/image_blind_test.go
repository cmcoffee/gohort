package core

// The caption pass stored whatever the model said, so a deployment whose model
// has no image modality wrote its refusal into the image ring as the picture's
// own description. An agent then read its manifest, found "no image appears to
// be attached" against a render that had SUCCEEDED, and told the user the image
// backend was broken — a false failure report sourced entirely from the
// framework's own bookkeeping.

import "testing"

func TestModelSawNoImageCatchesTheAnswersThatPolluteCaptions(t *testing.T) {
	// Verbatim from a live session's image ring.
	blind := []string{
		"I don't see an image in our conversation — it looks like the attachment didn't come through. Could you try uploading it again?",
		"no image appears to be attached",
		"There is no image provided, so I cannot describe it.",
		"As a text-based model I can't process images.",
	}
	for _, a := range blind {
		if !ModelSawNoImage(a) {
			t.Errorf("this would be stored as a picture's description: %q", a)
		}
	}

	// A real caption must survive, including one that describes an image whose
	// CONTENT is about absence — the obvious way to over-match.
	real := []string{
		"Suburban house. A two-story home with gray siding and a red front door.",
		"Empty room. No furniture is visible and the walls are bare.",
		"Screenshot. A terminal showing an error: image not found.",
		"A blank white square with nothing in it.",
	}
	for _, a := range real {
		if ModelSawNoImage(a) {
			t.Errorf("a genuine caption was discarded as a blind answer: %q", a)
		}
	}
}
