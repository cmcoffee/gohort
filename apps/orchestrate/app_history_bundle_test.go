package orchestrate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// An app's history travels as a kind of its own, offered beside the app:
// what is committed, as a git bundle. Imported with a fresh copy of the
// app, it becomes the start of that app's history, every version and who
// made it, with the app as it was imported on top. An app that was here
// before keeps its own, and a bundle that does not read is said, not taken.
func TestAnAppsHistoryTravelsWhenAskedFor(t *testing.T) {
	_, list := shelfVersions(t)
	plan, err := ArtifactExportPlanAsUser(RootDB, "u", []ArtifactSel{{Type: "custom_app", Name: "shelf"}})
	offered := false
	for _, d := range plan {
		if d.Type == appHistoryArtifactType && d.Name == "shelf" {
			offered = true
		}
	}
	if err != nil || !offered {
		t.Fatalf("the app's history is not offered beside it: %v %+v", err, plan)
	}
	recipe, err := appHistoryArtifact{}.ExportArtifact(nil, "shelf", "u")
	if err != nil {
		t.Fatal(err)
	}
	var r appHistoryRecipe
	if json.Unmarshal(recipe, &r) != nil || r.Schema != 1 || r.Name != "shelf" || r.Bundle == "" {
		t.Fatalf("the recipe: %s", recipe)
	}
	if after, _ := AppHistoryLog("u", "shelf", 0); len(after) != len(list) {
		t.Errorf("exporting the history changed it: %d versions, was %d", len(after), len(list))
	}

	// v imports the app (a fresh copy, as the app's own import makes it),
	// then its history.
	page := json.RawMessage(`[{"id":"page","kind":"html","html":"<!DOCTYPE html><html><body><h1>Novels</h1><p>v's copy</p></body></html>"}]`)
	SaveAppSpec(AppSpec{Owner: "v", Slug: "shelf", Name: "Shelf", RecordKey: "id", Sections: page, Page: json.RawMessage(`{"title":"Shelf","sections":[]}`)})
	if _, why, err := (appHistoryArtifact{}).ImportArtifact(nil, recipe, "v"); err != nil || why != "" {
		t.Fatalf("the import: %q %v", why, err)
	}
	got, err := AppHistoryLog("v", "shelf", 0)
	if err != nil || len(got) != len(list)+1 {
		t.Fatalf("v's history: %v %+v", err, got)
	}
	if got[0].Subject != "Imported here" || got[0].Author != "v" || !got[0].Live {
		t.Errorf("the import on top: %+v", got[0])
	}
	if got[1].Subject != "Say novels" || got[1].Author != "u" || got[2].Subject != "Start of Shelf's history" {
		t.Errorf("the imported versions, with who made them: %+v", got[1:])
	}
	if diff, _ := AppHistoryDiff("v", "shelf", got[0].Short); !strings.Contains(diff, "v's copy") {
		t.Errorf("the import's own version is not the app as imported:\n%s", diff)
	}
	if dir, _ := appHistoryDir("v", "shelf"); dir != "" {
		if st, _ := appRepoGit(dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
			t.Errorf("v's history is not on the app as it is: %q", st)
		}
	}

	// An app that was already here keeps the history it has.
	spec, _ := LoadAppSpec("v", "shelf")
	spec.Created = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	SaveAppSpec(spec)
	before, _ := AppHistoryLog("v", "shelf", 0)
	if _, why, _ := (appHistoryArtifact{}).ImportArtifact(nil, recipe, "v"); !strings.Contains(why, "already here") {
		t.Errorf("a history imported onto an app that was here: %q", why)
	}
	if now, _ := AppHistoryLog("v", "shelf", 0); len(now) != len(before) {
		t.Errorf("the app's own history was replaced: %d versions, was %d", len(now), len(before))
	}

	// A bundle that does not read.
	SaveAppSpec(AppSpec{Owner: "w", Slug: "shelf", Name: "Shelf", RecordKey: "id", Sections: page, Page: json.RawMessage(`{"title":"Shelf","sections":[]}`)})
	bad, _ := json.Marshal(appHistoryRecipe{Schema: 1, Name: "shelf", Bundle: base64.StdEncoding.EncodeToString([]byte("not a bundle"))})
	if _, why, err := (appHistoryArtifact{}).ImportArtifact(nil, bad, "w"); err != nil || !strings.Contains(why, "does not read") {
		t.Errorf("a bundle that does not read: %q %v", why, err)
	}
	if w, _ := AppHistoryLog("w", "shelf", 0); len(w) != 1 {
		t.Errorf("a bad bundle touched the app's own history: %+v", w)
	}
}
