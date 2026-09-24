package customapps

// An app's rows travel only when asked for, only the owner's own, and land only
// in an empty table of the importer's own copy of the app.

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAppRecordsTravelOnRequestIntoAnEmptyCopy(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })

	T := &CustomApps{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.registerRecordsArtifact()
	spec := SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id"})
	T.recordBase(spec, "alice").Set(recTable("tally"), "r1", map[string]any{"id": "r1", "count": 3})
	// A visitor's row in their own sub-store: never alice's to export.
	T.recordBase(spec, "carol").Set(recTable("tally"), "c1", map[string]any{"id": "c1", "count": 99})

	// Plain export of the app: no rows.
	b, err := ExportArtifactBundleAsUser(RootDB, "alice", []ArtifactSel{{Type: "custom_app", Name: "tally"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range b.Artifacts {
		if a.Type == "app_records" {
			t.Fatal("rows travelled without being asked for")
		}
	}
	b, err = ExportArtifactBundleAsUser(RootDB, "alice", []ArtifactSel{{Type: "custom_app", Name: "tally"}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"app_records"}})
	if err != nil {
		t.Fatal(err)
	}
	var rows appRecordsRecipe
	for _, a := range b.Artifacts {
		if a.Type == "app_records" {
			_ = json.Unmarshal(a.Recipe, &rows)
		}
	}
	if len(rows.Records) != 1 || rows.Records[0]["id"] != "r1" {
		t.Fatalf("only alice's own row should travel: %+v", rows.Records)
	}

	data, _ := json.Marshal(b)
	res, err := ImportArtifactBundleAsUser(RootDB, data, "bob")
	if err != nil || res.Imported != 2 {
		t.Fatalf("import: %v %+v", err, res.Outcomes)
	}
	bspec, ok := LoadAppSpec("bob", "tally")
	if !ok {
		t.Fatal("the app did not land")
	}
	var got map[string]any
	if !T.recordBase(bspec, "bob").Get(recTable("tally"), "r1", &got) {
		t.Fatal("the row did not land in bob's copy")
	}
	// Again: bob's table has rows now, so nothing is merged.
	res, _ = ImportArtifactBundleAsUser(RootDB, data, "bob")
	for _, o := range res.Outcomes {
		if o.Type == "app_records" && o.Status != "skipped" {
			t.Errorf("rows merged into a table that already had some: %+v", o)
		}
	}
}
