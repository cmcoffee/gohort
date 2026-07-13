package core

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// skillTestDB returns an in-memory Database and pins RootDB to it for the
// test's duration — skillStore prefers RootDB, so without the pin a test's
// writes could land somewhere another test (or nothing) reads.
func skillTestDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

func TestSkillArtifact_ExportStripsIdentity(t *testing.T) {
	db := skillTestDB(t)
	saved, err := SaveSkill(db, "alice", SkillRecord{
		Name:         "pdf-handling",
		Description:  "Use when handling PDFs.",
		Instructions: "Always extract text first.",
		Triggers:     []string{"*.pdf"},
		Embedding:    []float32{0.1, 0.2},
		Disabled:     true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("SaveSkill should assign an ID")
	}

	recipe, err := skillArtifact{}.ExportArtifact(db, "pdf-handling", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got SkillRecord
	if err := json.Unmarshal(recipe, &got); err != nil {
		t.Fatalf("recipe unmarshal: %v", err)
	}
	if got.ID != "" || got.Owner != "" || got.Embedding != nil || got.Disabled {
		t.Fatalf("identity fields must not travel: %+v", got)
	}
	if !got.Created.IsZero() || !got.Updated.IsZero() {
		t.Fatalf("timestamps must not travel: %+v", got)
	}
	if got.Name != "pdf-handling" || got.Instructions != "Always extract text first." {
		t.Fatalf("skill shape must travel intact: %+v", got)
	}

	if _, err := (skillArtifact{}).ExportArtifact(db, "pdf-handling", ""); err == nil {
		t.Fatal("export without an owner must error")
	}
	if _, err := (skillArtifact{}).ExportArtifact(db, "nope", "alice"); err == nil {
		t.Fatal("export of a missing skill must error")
	}
}

func TestSkillArtifact_ImportLandsDisabledDraft(t *testing.T) {
	db := skillTestDB(t)
	recipe, _ := json.Marshal(SkillRecord{
		Name:         "reviewer",
		Description:  "Use when reviewing.",
		Instructions: "Be thorough.",
	})

	name, skip, err := skillArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || skip != "" || name != "reviewer" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	got, ok := FindSkillByName(db, "bob", "reviewer")
	if !ok {
		t.Fatal("imported skill not found in owner's pool")
	}
	if !got.Disabled {
		t.Fatal("an imported skill must land disabled for review")
	}
	if got.ID == "" || got.Owner != "bob" {
		t.Fatalf("import must mint fresh identity under the owner: %+v", got)
	}

	// Same name again → skip, never clobber.
	_, skip, err = skillArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || skip == "" {
		t.Fatalf("same-named import must skip: skip=%q err=%v", skip, err)
	}
}

func TestSkillArtifact_Dependencies(t *testing.T) {
	db := skillTestDB(t)
	if _, err := SaveSkill(db, "alice", SkillRecord{
		Name:        "ops",
		Description: "Use for ops.",
		// Names a built-in / unknown tool — not an exportable temp tool, so it
		// contributes no dependency.
		AllowedTools: []string{"web_search"},
		// A bundled api-mode tool dispatches through a credential by NAME.
		Tools: []TempTool{
			{Name: "pager", Credential: "pagerduty"},
			{Name: "calc", Credential: "no_auth"}, // bootstrap sentinel — filtered
		},
		AttachedCollections: []string{
			"coll-123",
			DeploymentKnowledgeCollectionID, // exists everywhere — never a dependency
		},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	deps := skillArtifact{}.Dependencies(db, "ops", "alice")
	want := map[string]bool{"credential\x00pagerduty": true, "collection\x00coll-123": true}
	if len(deps) != len(want) {
		t.Fatalf("expected %d deps, got %v", len(want), deps)
	}
	for _, d := range deps {
		if !want[d.Type+"\x00"+d.Name] {
			t.Fatalf("unexpected dependency %s/%s in %v", d.Type, d.Name, deps)
		}
	}
}

func TestSkillArtifact_ListEnumeratesAllOwners(t *testing.T) {
	db := skillTestDB(t)
	mustSave := func(user, name string) {
		t.Helper()
		if _, err := SaveSkill(db, user, SkillRecord{Name: name, Description: "d"}); err != nil {
			t.Fatalf("save %s/%s: %v", user, name, err)
		}
	}
	mustSave("alice", "a1")
	mustSave("bob", "b1")

	sels := skillArtifact{}.ListArtifacts(db)
	found := map[string]bool{}
	for _, s := range sels {
		if s.Type != "skill" {
			t.Fatalf("wrong type in %v", s)
		}
		found[s.Owner+"/"+s.Name] = true
	}
	if !found["alice/a1"] || !found["bob/b1"] {
		t.Fatalf("expected both owners' skills, got %v", sels)
	}
}
