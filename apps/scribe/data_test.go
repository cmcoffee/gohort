package scribe

import (
	"strings"
	"testing"
)

func TestRenderGuideHTML(t *testing.T) {
	g := Guide{
		ID: "g1", Title: "K8s Guide", Subtitle: "A primer",
		Sections: []Section{
			{ID: "b", Title: "Setup", Markdown: "Install with `kubectl`.", Order: 2},
			{ID: "a", Title: "Intro", Markdown: "### Why\nIt orchestrates containers.", Order: 1},
			{ID: "c", Title: "Next", Markdown: "Scale out.", Order: 3},
		},
	}
	html := renderGuideHTML(g, true)
	// Title + subtitle.
	for _, want := range []string{"K8s Guide", "A primer"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Table of contents present: three sections is where a guide gets one.
	if !strings.Contains(html, "guide-toc") {
		t.Error("no table of contents")
	}
	// Ordered: Intro (order 1) before Setup (order 2).
	if strings.Index(html, "Intro") > strings.Index(html, "Setup") {
		t.Error("sections not ordered by Order")
	}
	// Markdown rendered to HTML (inline code → <code>, ### → heading).
	if !strings.Contains(html, "<code>kubectl</code>") {
		t.Errorf("inline code not rendered:\n%s", html)
	}
	// Anchors link ToC to sections.
	if !strings.Contains(html, `id="sec-1"`) || !strings.Contains(html, `href="#sec-1"`) {
		t.Error("ToC anchors missing")
	}
}

func TestRenderGuideHTML_Empty(t *testing.T) {
	html := renderGuideHTML(Guide{ID: "x", Title: "Empty"}, true)
	if !strings.Contains(html, "no sections yet") {
		t.Errorf("empty guide should prompt to add a section:\n%s", html)
	}
}

func TestGuideNextOrder(t *testing.T) {
	g := Guide{Sections: []Section{{Order: 1}, {Order: 5}, {Order: 3}}}
	if got := g.nextOrder(); got != 6 {
		t.Errorf("nextOrder = %d, want 6", got)
	}
	if got := (Guide{}).nextOrder(); got != 1 {
		t.Errorf("empty nextOrder = %d, want 1", got)
	}
}

// A co-editor restoring an older version of a guide brings back its text,
// not the owner's choices: Private, its sources, knowledge and brief, its
// title. The owner restoring brings back everything but who owns it and who
// it is shared with.
func TestARestoreKeepsTheOwnersChoices(t *testing.T) {
	cur := Guide{ID: "g1", Owner: "alice", Title: "Now", Private: true, Brief: "now", Collections: []string{"c2"},
		Shared: true, AllowedUsers: []string{"bob"}, Editors: []string{"bob"}}
	old := Guide{ID: "g1", Owner: "alice", Title: "Then", Private: false, Brief: "then", Collections: []string{"c1"}}
	byEditor := old
	keepGuideOwnership(&byEditor, cur, false)
	if !byEditor.Private || byEditor.Brief != "now" || byEditor.Title != "Now" || byEditor.Collections[0] != "c2" {
		t.Errorf("a co-editor's restore undid the owner's settings: %+v", byEditor)
	}
	if !byEditor.Shared || len(byEditor.Editors) != 1 {
		t.Errorf("a restore changed who the guide is shared with: %+v", byEditor)
	}
	byOwner := old
	keepGuideOwnership(&byOwner, cur, true)
	if byOwner.Private || byOwner.Brief != "then" || !byOwner.Shared || len(byOwner.AllowedUsers) != 1 {
		t.Errorf("the owner's restore: %+v", byOwner)
	}
}
