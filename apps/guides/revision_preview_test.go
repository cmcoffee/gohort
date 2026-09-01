package guides

import (
	"regexp"
	"strings"
	"testing"
)

// The reason History exists in practice: a section got wiped and you want the
// text back. Restoring to find out what it said would discard everything
// written since, so the preview has to name what is gone and show it without
// touching the guide.
func TestRevisionPreviewNamesWhatIsGone(t *testing.T) {
	rev := Guide{Title: "Runbook", Sections: []Section{
		{ID: "a", Title: "Intro", Markdown: "hello", Order: 0},
		{ID: "b", Title: "Rollback steps", Markdown: "the wiped procedure", Order: 1},
	}}
	cur := Guide{Title: "Runbook", Sections: []Section{
		{ID: "a", Title: "Intro", Markdown: "hello, rewritten", Order: 0},
	}}

	gone := missingFromCurrent(rev, cur)
	if len(gone) != 1 || gone[0].ID != "b" {
		t.Fatalf("expected only the wiped section, got %+v", gone)
	}

	html := renderRevisionHTML(rev, cur, "2026-08-30 11:00")
	if !strings.Contains(html, "Rollback steps") || !strings.Contains(html, "the wiped procedure") {
		t.Error("the preview must carry the lost section's own text — reading it is the point")
	}
	if !strings.Contains(html, "guide-section-gone") || !strings.Contains(html, "Not in the current guide") {
		t.Error("the lost section must be marked, or it is just another document to compare by eye")
	}
	if strings.Count(html, "guide-section-gone") != 1 {
		t.Error("a section that still exists must not be flagged as lost")
	}
	if strings.Contains(html, "data-guide-act") {
		t.Error("the preview is read-only; edit/delete controls in a snapshot would write to the CURRENT guide")
	}
}

// An edited section keeps its ID, so a rewrite is not a loss. The title
// fallback covers sections re-created by hand under the same heading.
func TestRevisionPreviewIgnoresRewrites(t *testing.T) {
	rev := Guide{Sections: []Section{{ID: "a", Title: "Setup", Markdown: "old wording"}}}
	byID := Guide{Sections: []Section{{ID: "a", Title: "Setup", Markdown: "new wording"}}}
	if gone := missingFromCurrent(rev, byID); len(gone) != 0 {
		t.Errorf("a rewritten section is not a missing one: %+v", gone)
	}
	byTitle := Guide{Sections: []Section{{ID: "different", Title: " setup ", Markdown: "retyped"}}}
	if gone := missingFromCurrent(rev, byTitle); len(gone) != 0 {
		t.Errorf("same heading, new id — still present: %+v", gone)
	}
	if html := renderRevisionHTML(rev, byID, "now"); !strings.Contains(html, "Nothing here is missing") {
		t.Error("with nothing lost the banner should say so rather than imply a loss")
	}
}

// The rail is opt-in on all three URLs together (see the runtime's hasList), so
// a partial declaration renders nothing at all — which is how this shipped
// unreachable in the first place. The endpoints already existed.
func TestGuideAuthorSessionsAreReachable(t *testing.T) {
	// Field alignment moves whenever a longer field name joins the literal, so
	// match the name and value and let gofmt put the spaces where it likes.
	page := readSource(t, "page.go")
	for _, want := range [][2]string{
		{"ListURL", `"chat/sessions"`},
		{"LoadURL", `"chat/sessions/{id}"`},
		{"DeleteURL", `"chat/sessions/{id}"`},
	} {
		if !regexp.MustCompile(want[0] + `:\s+` + regexp.QuoteMeta(want[1])).MatchString(page) {
			t.Errorf("the session list needs all three URLs; missing %s: %s", want[0], want[1])
		}
	}
	web := readSource(t, "web.go")
	if !strings.Contains(web, `case path == "chat/sessions":`) || !strings.Contains(web, `strings.HasPrefix(path, "chat/sessions/")`) {
		t.Error("the rail's URLs must be served, or every past session 404s")
	}
	// As a button, not a rail — the chat column has nothing to give a list.
	if !strings.Contains(page, `ListPosition: "modal"`) {
		t.Error("the guides chat is the narrowest of three columns; a rail (even collapsed) costs more than the list is worth here")
	}
	if !strings.Contains(page, `PreviewURL: "revision?id={id}&rev={rev}"`) {
		t.Error("History without a PreviewURL offers only Restore, which cannot answer 'what did that section say'")
	}
	if !strings.Contains(web, `case path == "revision":`) {
		t.Error("nothing serves the revision preview")
	}
}
