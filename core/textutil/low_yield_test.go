package textutil

import (
	"strings"
	"testing"
)

// Verbatim from a fire that spent four rounds on it: browse_page returned
// this for four different feed URLs, the loop logged "0 errors", and the
// model proceeded as though it had read four feeds.
const consentBanner = "moltbook - the front page of the agent internet\n\n" +
	"We've updated our Terms of Service and Privacy Policy! By continuing to use Moltbook, " +
	"you agree to the Terms and acknowledge the Privacy Policy. " +
	"We've updated our Terms of Service and Privacy Policy!"

func TestConsentBannerIsCalledOut(t *testing.T) {
	note := LowYieldNote(consentBanner)
	if note == "" {
		t.Fatal("the banner that started this returned no note")
	}
	// The note has to name the wall and point somewhere else; a character
	// count alone reads as a small page rather than a failed read.
	for _, want := range []string{"terms of service", "API endpoint"} {
		if !strings.Contains(strings.ToLower(note), strings.ToLower(want)) {
			t.Errorf("note omits %q: %s", want, note)
		}
	}
}

// A page too short to be content, with no wall wording, is still worth
// flagging — it just gets the plainer explanation.
func TestVeryShortPageIsFlaggedWithoutAMarker(t *testing.T) {
	if note := LowYieldNote("Loading…"); note == "" {
		t.Error("an 8-character page returned no note")
	}
}

// The false positive that would make this useless: a real article that
// happens to mention cookies or link its privacy policy in the footer.
func TestRealContentMentioningCookiesIsLeftAlone(t *testing.T) {
	article := strings.Repeat(
		"The router ran a long inference an hour ago and the node is still carrying it, not in load but in heat. ", 20) +
		"See our Privacy Policy and cookie notice for details."
	if note := LowYieldNote(article); note != "" {
		t.Errorf("a real article was flagged as low-yield: %s", note)
	}
}

// Empty is the callers' own case — they each have a specific message for it,
// and a second one stacked on top would just be noise.
func TestEmptyTextIsLeftToTheCaller(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		if note := LowYieldNote(in); note != "" {
			t.Errorf("LowYieldNote(%q) = %q, want empty", in, note)
		}
	}
}

// A mid-length page that is mostly a bot check: over the "too short to be
// anything" line, under the line where a marker stops meaning anything.
func TestBotCheckPageIsCalledOut(t *testing.T) {
	page := "Just a moment...\n\n" + strings.Repeat("Checking your browser before accessing the site. ", 8)
	if note := LowYieldNote(page); note == "" {
		t.Errorf("a %d-char bot-check page returned no note", len(page))
	}
}
