package core

import (
	"strings"
	"testing"
)

// An agent that had just patched one of its own apps tried to LOOK at it:
// /custom/<slug>/ against fetch_url, then browse_page, then a guessed public
// hostname that 404'd, then treating the app as an agent — six tool errors,
// none of which pointed at the two tools that do this. The refusal was right;
// it just answered a question nobody asked.

func TestHintNamesTheAppAndTheTools(t *testing.T) {
	got := SameOriginURLHint("/custom/flappy-lawyer/")
	if !strings.Contains(got, `app_def(action="verify", id="flappy-lawyer")`) {
		t.Errorf("should name verify WITH the slug filled in: %q", got)
	}
	if !strings.Contains(got, `show_html(url="/custom/flappy-lawyer/")`) {
		t.Errorf("should offer show_html for displaying it: %q", got)
	}
	// A sub-path of an app still identifies the app.
	if !strings.Contains(SameOriginURLHint("/custom/my-game/data/scores"), `id="my-game"`) {
		t.Error("a sub-path should still resolve the slug")
	}
}

func TestHintCoversOtherSameOriginPaths(t *testing.T) {
	got := SameOriginURLHint("/admin/api/routing")
	if got == "" {
		t.Fatal("any same-origin path deserves the explanation")
	}
	if strings.Contains(got, "app_def") {
		t.Errorf("a non-app path must not claim to be an app: %q", got)
	}
	if !strings.Contains(got, "show_html") {
		t.Errorf("should still point at the way to display it: %q", got)
	}
}

// An ordinary typo must keep the plain message — a hint on everything is a
// hint on nothing.
func TestHintStaysQuietForRealURLs(t *testing.T) {
	for _, in := range []string{
		"https://example.com/x",
		"http://example.com",
		"example.com/custom/foo/",        // no leading slash — a bare hostname typo
		"//evil.example.com/custom/foo/", // protocol-relative: genuinely external
		"ftp://example.com",
		"",
		"   ",
	} {
		if got := SameOriginURLHint(in); got != "" {
			t.Errorf("%q should get no hint, got %q", in, got)
		}
	}
}

func TestCustomAppSlugEdges(t *testing.T) {
	if got := customAppSlug("/custom/"); got != "" {
		t.Errorf("/custom/ alone names no app, got %q", got)
	}
	// The public capability-URL route is not an app slug.
	if got := customAppSlug("/custom/pub/abc123/"); got != "" {
		t.Errorf("the pub route is not an app slug, got %q", got)
	}
	if got := customAppSlug("/custom/slug"); got != "slug" {
		t.Errorf("a trailing-slash-less path should still resolve, got %q", got)
	}
}
