package scribe

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Another app can link a source to one of the person's own guides, as the
// guide's Sources picker would: added once, without touching the guide's
// content, and never to a guide that is not theirs.
func TestASourceCanBeAttachedToAnOwnGuide(t *testing.T) {
	app := &Scribe{}
	app.DB = testStore(t)
	target := &guideTarget{app: app}
	udb := UserDB(app.DB, "u")
	saveGuide(udb, Guide{ID: "g1", Owner: "u", Title: "Runbook", Sections: []Section{{ID: "s1", Title: "Start", Markdown: "Begin here.", Order: 1}}})

	for i := 0; i < 2; i++ {
		if err := target.AttachReference("u", "g1", "system", "box-1"); err != nil {
			t.Fatalf("attach: %v", err)
		}
	}
	g, _ := loadGuide(udb, "g1")
	if len(g.References) != 1 || g.References[0] != (ReferenceSelection{Kind: "system", ItemID: "box-1"}) {
		t.Fatalf("the source is attached once, read-only: %+v", g.References)
	}
	if len(g.Sections) != 1 || g.Sections[0].Markdown != "Begin here." {
		t.Fatalf("attaching leaves the content alone: %+v", g.Sections)
	}
	if got := target.ListReferencing("u", "system", "box-1"); len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("the guide now lists as referencing the system: %+v", got)
	}

	if err := target.AttachReference("v", "g1", "system", "box-1"); err == nil {
		t.Error("another person cannot attach to a guide that is not theirs")
	}
	saveGuide(udb, Guide{ID: "g2", Owner: "someone-else", Title: "Theirs"})
	if err := target.AttachReference("u", "g2", "system", "box-1"); err == nil {
		t.Error("a guide owned by someone else is refused")
	}
}
