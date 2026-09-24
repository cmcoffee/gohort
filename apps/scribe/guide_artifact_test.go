package scribe

// A guide travels losslessly and lands private: every section, the subtitle,
// attached knowledge and sources come back; the id, sharing and publish history
// do not.

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAGuideRoundTripsLosslesslyAndLandsPrivate(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	ga := &guideArtifact{app: T}
	orig := Guide{
		ID: newID(), Title: "Runbook", Subtitle: "Ops", Owner: "alice",
		Sections: []Section{
			{ID: "s2", Title: "Second", Markdown: "two", Order: 2},
			{ID: "s1", Title: "First", Markdown: "one", Order: 1},
		},
		Collections: []string{"coll-1"},
		References:  []ReferenceSelection{{Kind: "filestore", ItemID: "docs"}},
		Shared:      true, ShareMode: "edit",
		Published: []docs.PublishRecord{{}},
	}
	saveGuide(UserDB(T.DB, "alice"), orig)

	recipe, err := ga.ExportArtifact(nil, orig.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	_ = json.Unmarshal(recipe, &probe)
	for _, k := range []string{"id", "owner", "shared", "share_mode", "published"} {
		if _, ok := probe[k]; ok {
			t.Errorf("the recipe carries %q, which describes this install", k)
		}
	}
	if deps := ga.Dependencies(nil, orig.ID, "alice"); len(deps) != 2 {
		t.Errorf("collection and source should be declared: %+v", deps)
	}

	name, skip, err := ga.ImportArtifact(nil, recipe, "bob")
	if err != nil || skip != "" || name != "Runbook" {
		t.Fatalf("import: %q %q %v", name, skip, err)
	}
	got, ok := ga.find("bob", "Runbook")
	if !ok {
		t.Fatal("not in bob's library")
	}
	if got.ID == orig.ID || got.Owner != "bob" || got.Shared || got.ShareMode != "" || !got.Private || len(got.Published) != 0 {
		t.Errorf("not inert: %+v", got)
	}
	secs := got.sorted()
	if len(secs) != 2 || secs[0].Title != "First" || secs[1].Markdown != "two" || got.Subtitle != "Ops" {
		t.Errorf("content lost: %+v", secs)
	}
	if len(got.Collections) != 1 || len(got.References) != 1 {
		t.Errorf("attachments lost: %+v %+v", got.Collections, got.References)
	}
	if _, skip, _ := ga.ImportArtifact(nil, recipe, "bob"); skip == "" {
		t.Error("a second import of the same title should skip")
	}
	// Alice cannot export bob's copy by naming it.
	if _, err := ga.ExportArtifact(nil, got.ID, "alice"); err == nil {
		t.Error("an export reached another user's guide by id")
	}
}
