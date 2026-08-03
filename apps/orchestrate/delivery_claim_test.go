// Recognizing a delivery the model THINKS it made. This gates the backstop that
// ships a file the reply promised and nothing attached — the "here's your
// image" with no image that kept coming back.
package orchestrate

import "testing"

func TestADeliveryClaimIsRecognizedHoweverItIsPhrased(t *testing.T) {
	for _, claim := range []string{
		"Here's your picture of a house!",
		"Here is the image you asked for.",
		"here you go — one haunted mansion, photo attached",
		"I've attached the screenshot.",
		"Sending you the pdf now.",
		// The phrasings that actually come back and used to sit outside the
		// cue list entirely — a model announcing what it MADE is claiming
		// delivery as much as one saying "here you go".
		"Done — your haunted house image is ready!",
		"I made you a picture of a cat 🐱",
		"I've generated the image.",
		"All set! One spooky mansion picture for you.",
	} {
		if !replyClaimsAttachment(claim) {
			t.Errorf("not recognized as a delivery claim: %q", claim)
		}
	}
}

func TestAFailureIsNotADeliveryClaim(t *testing.T) {
	// The dangerous false positive: a reply that says it went WRONG still
	// mentions a picture, and shipping a staged file on the back of it hands
	// the user the very thing the model just rejected.
	for _, notClaim := range []string{
		"I couldn't find a picture that matches.",
		"I wasn't able to generate the image.",
		"I found an image but it doesn't match what you described.",
		"No luck on the photo — the sources blocked the download.",
		"Failed to create the picture.",
	} {
		if replyClaimsAttachment(notClaim) {
			t.Errorf("a failure was read as a delivery claim: %q", notClaim)
		}
	}
}

func TestOrdinaryTalkIsNotADeliveryClaim(t *testing.T) {
	for _, plain := range []string{
		"Here's what I think about that.",
		"I made a reservation for 7pm.",
		"The house is on Elm Street.",
		"",
	} {
		if replyClaimsAttachment(plain) {
			t.Errorf("ordinary text read as a delivery claim: %q", plain)
		}
	}
}
