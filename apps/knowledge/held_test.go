package knowledge

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Held pages are for whoever decides about the collection, as its
// suggestions are: the owner and a maintainer, not a reader. Each row offers
// the three ways to settle it by hand, the replace only where there is a
// document to replace, and Settle it with its spinner, progress and Stop.
func TestHeldPagesOfferEveryWayToSettleThem(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob", "carol"}, Maintainers: []string{"bob"}})
	s, ok := app.heldSection("alice", "col-1")
	if !ok {
		t.Fatal("the owner has no held pages section")
	}
	if _, ok := app.heldSection("bob", "col-1"); !ok {
		t.Error("a maintainer does not see the held pages")
	}
	if _, ok := app.heldSection("carol", "col-1"); ok {
		t.Error("a reader sees the held pages")
	}
	blob, _ := json.Marshal(s.Body)
	out := string(blob)
	for _, want := range []string{
		`"source":"/orchestrate/api/collections/col-1/held"`,
		`"post_to":"/orchestrate/api/collections/col-1/held/add?sid={id}"`,
		`"only_if":"has_doc"`,
		`"type":"expand","label":"Settle it"`,
		`"only_if":"can_settle"`,
		`"label":"Leave it out"`,
		`"hide_if":"has_doc"`,
		`"post_to":"/orchestrate/api/collections/col-1/held/replace?sid={id}"`,
		`"post_to":"/orchestrate/api/collections/col-1/held/keep?sid={id}"`,
		`/held/settle/progress?sid={id}`,
		`/held/settle/stop?sid={id}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the held pages section has no %s:\n%s", want, out)
		}
	}
	if strings.Contains(s.Detail+s.Subtitle, "\u2014") || strings.Contains(s.Detail+s.Subtitle, "\u2026") {
		t.Error("an em-dash or ellipsis character in the section's copy")
	}
}
