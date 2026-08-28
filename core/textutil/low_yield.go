// Shared knowledge: telling a page that HAS no content apart from one that
// merely renders it somewhere the fetcher cannot see.
//
// Lives here beside the JS-heavy domain list, and for the same reason: the
// LLM-side fetch_url and the browser-side browse_page both need the same
// answer, and a copy in each is a copy that drifts.
//
// The problem this exists for: a fetch of a client-rendered or consent-walled
// page succeeds. HTTP 200, no error, a couple of hundred characters of cookie
// notice. The agent loop logs "0 errors", the model is handed something that
// reads like a result, and it believes it read the page — then reasons from
// nothing, or burns rounds fetching three more pages the same way. Nothing in
// the transcript says otherwise, because by every mechanical measure the call
// worked.
//
// So the shortfall is named in the result itself, where the model will read
// it. It is a note, not an error: the fetch DID succeed, and a genuinely short
// page is a legitimate answer.
package textutil

import (
	"fmt"
	"strings"
)

const (
	// emptyishMaxChars — below this, a page has said nothing whatever its
	// wording. Set under the length of a short blog paragraph so ordinary
	// terse pages (a 404 body, a status line) are still described honestly
	// rather than dressed up as content.
	emptyishMaxChars = 220
	// boilerplateMaxChars — up to here, ONE consent/JS marker is enough to
	// call it. Above it, a page that merely mentions cookies in its footer is
	// a real page and gets left alone; the marker only means something when
	// it is most of what came back.
	boilerplateMaxChars = 1200
)

// wallMarkers are phrases that mean "you are looking at the wall, not the
// page". Lowercase; matched as substrings.
//
// Kept to phrases that are unambiguous ABOUT THE FETCH: a consent gate, a
// JS requirement, a bot check, a login wall. Not "subscribe" or "sign up",
// which appear on plenty of pages that also served their content.
var wallMarkers = []string{
	"terms of service",
	"privacy policy",
	"cookie",
	"consent",
	"accept all",
	"enable javascript",
	"javascript is required",
	"requires javascript",
	"javascript to run",
	"log in to continue",
	"sign in to continue",
	"verify you are human",
	"verifying you are human",
	"are you a robot",
	"checking your browser",
	"access denied",
}

// LowYieldNote describes what is wrong with a fetch that returned almost
// nothing, or "" when the text looks like real content.
//
// The note names the mechanism (client-side rendering, a consent gate) and
// what to do instead, because "got 248 characters" on its own reads as a
// small page rather than a failed read.
func LowYieldNote(text string) string {
	t := strings.TrimSpace(text)
	n := len(t)
	if n == 0 {
		return "" // callers already have a dedicated message for a wholly empty read
	}
	marker := firstWallMarker(t)
	switch {
	case n < emptyishMaxChars:
		if marker != "" {
			return fmt.Sprintf("this is %d characters and is mostly a %s notice — the page's real content did not load. "+
				"It almost certainly renders client-side or sits behind a consent gate. Look for an API endpoint on this host, "+
				"or a tool that serves it, rather than fetching the page again", n, marker)
		}
		return fmt.Sprintf("this is only %d characters — too little to be the page's content. "+
			"It likely renders client-side. Look for an API endpoint on this host, or a tool that serves it, "+
			"rather than fetching the page again", n)
	case n < boilerplateMaxChars && marker != "":
		return fmt.Sprintf("this is %d characters and is dominated by a %s notice — what came back is the wall, not the page. "+
			"Look for an API endpoint on this host, or a tool that serves it, rather than fetching the page again", n, marker)
	}
	return ""
}

// firstWallMarker returns the first wall phrase present in t (already
// trimmed), or "" for none. The phrase itself goes into the note so the
// message says WHICH wall, which is what makes it actionable.
func firstWallMarker(t string) string {
	lower := strings.ToLower(t)
	for _, m := range wallMarkers {
		if strings.Contains(lower, m) {
			return m
		}
	}
	return ""
}
