package imagefetch

// A vision screen that cannot see is not a screen that says no.
//
// Asked to rate how well a picture depicts "house", a model with no image
// modality answers that it cannot see any image and then, obediently, rates it
// 0. That reads as a genuine rejection, so every candidate "fails" and the
// search reports "none clearly depict it (best visual match 0/100) — refine the
// query" while holding perfectly good photographs of houses. The caller then
// rewords a query that was never the problem.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestABlindScreenIsAnAbstentionNotAZero(t *testing.T) {
	blind := []string{
		"I don't see an image in your message. Could you attach it?\n0",
		"There is no image provided, so I cannot describe it. 0",
		"I'm unable to see images. As a text-based model I can only read text.\n0",
		"I cannot process images, but based on the description: 0",
	}
	for _, answer := range blind {
		if !ModelSawNoImage(answer) {
			t.Errorf("not recognized as a blind screen, so its 0 will be read as a rejection: %q", answer)
		}
	}

	// A screen that actually looked must never be downgraded, at either end of
	// the range — a real 0 is a real rejection and has to keep rejecting.
	sighted := []string{
		"The image shows a two-story suburban house with a green lawn.\n95",
		"This is a photograph of a cat sitting on a windowsill, not a house.\n0",
		"A close-up of a bicycle wheel against gravel. 12",
		"The image shows an empty room with no furniture.\n30",
	}
	for _, answer := range sighted {
		if ModelSawNoImage(answer) {
			t.Errorf("a screen that described the picture was treated as blind: %q", answer)
		}
	}
}

// The sentinel has to sort below both a real rating and a missing one, or the
// loop's existing arithmetic quietly promotes it: `score >= 0` would count it
// as rated, and `score > bestScore` would let it become the best candidate.
func TestBlindSentinelCountsAsNeitherRatedNorBest(t *testing.T) {
	if scoreScreenBlind >= 0 {
		t.Error("a blind screen would be counted as having rated the candidate")
	}
	if bestScore := -1; scoreScreenBlind > bestScore {
		t.Error("a blind screen would become the best match, beating an honest abstention")
	}
	if scoreScreenBlind >= imageMatchThreshold {
		t.Error("a blind screen would pass the acceptance threshold")
	}
}
