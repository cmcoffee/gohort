package imagefetch

// What a search does when nothing scored a confident match. Three endings, not
// two — and the one that was missing is the one a request for a picture of a
// specific PERSON lands on.

import "testing"

func TestAnAbstainingScreenIsNotADownloadFailure(t *testing.T) {
	// Asking a vision model to confirm a named person's identity is the
	// question it most often declines: it answers in prose with no trailing
	// rating, every candidate comes back unscored, and the search reported
	// "could not download any usable image" — sending you to debug a network
	// problem that never happened, while six good photographs sat in hand.
	if got := findOutcome(6, 0, -1); got != findScreenAbstained {
		t.Errorf("six images fetched, none rated → %v, want findScreenAbstained", got)
	}
	// Nothing downloaded at all is the real download failure.
	if got := findOutcome(0, 0, -1); got != findNoImages {
		t.Errorf("no usable images → %v, want findNoImages", got)
	}
	// Rated and genuinely wrong stays a rejection — that message is accurate
	// and worth keeping.
	if got := findOutcome(4, 4, 20); got != findAllRejected {
		t.Errorf("four rated, best 20/100 → %v, want findAllRejected", got)
	}
	// A partial screen still counts as screened: something answered.
	if got := findOutcome(5, 1, 10); got != findAllRejected {
		t.Errorf("one of five rated → %v, want findAllRejected", got)
	}
}

func TestAnUnratedReplyScoresAsNoAnswer(t *testing.T) {
	// The mechanism behind the above: a decline carries no number, and "no
	// answer" must not read as "rated zero".
	if got := parseTrailingScore("I can't identify people in photographs."); got != -1 {
		t.Errorf("a refusal scored %d, want -1 (no answer)", got)
	}
	if got := parseTrailingScore("A person standing outdoors.\n0"); got != 0 {
		t.Errorf("an explicit zero scored %d, want 0", got)
	}
	if got := parseTrailingScore("Looks right.\n95"); got != 95 {
		t.Errorf("scored %d, want 95", got)
	}
}
