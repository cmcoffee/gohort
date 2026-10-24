package scribe

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A report pushed as a new guide arrives as its parts: one section each, in
// order, the brief set, and all of it one revision so one undo takes it back.
// Empty parts are left out; a push with nothing in it makes no guide.
func TestANewGuideIsMadeFromWholeSections(t *testing.T) {
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	tgt := &guideTarget{app: T}
	id, err := tgt.CreateWithSections(context.Background(), "alice", "Health Risks and Benefits of Raw Milk", "Keep to raw milk.", []docs.DocSection{
		{Title: "Executive Summary", Text: "It carries risk."},
		{Title: "", Text: "Untitled part."},
		{Title: "Empty", Text: "   "},
		{Title: "Sources", Text: "- **[1]** [A study](https://example.org/a)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	udb := UserDB(T.DB, "alice")
	g, ok := loadGuide(udb, id)
	if !ok || g.Title != "Health Risks and Benefits of Raw Milk" || g.Brief != "Keep to raw milk." || g.Owner != "alice" {
		t.Fatalf("guide: %+v", g)
	}
	var titles []string
	for _, s := range g.Sections {
		titles = append(titles, s.Title)
	}
	if len(titles) != 3 || titles[0] != "Executive Summary" || titles[1] != "Section" || titles[2] != "Sources" {
		t.Errorf("sections: %v", titles)
	}
	if n := len(listRevisions(udb, id)); n != 1 {
		t.Errorf("one push should be one revision, got %d", n)
	}
	if _, err := tgt.CreateWithSections(context.Background(), "alice", "x", "", []docs.DocSection{{Title: "a", Text: " "}}); err == nil {
		t.Error("a push with nothing in it made a guide")
	}
}
