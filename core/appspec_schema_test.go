package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Every save stamps the current schema; a pre-stamp record reads as schema 1.
func TestAppSpecSchemaStampedOnSave(t *testing.T) {
	verifyTestStore(t)
	if (AppSpec{}).SchemaVersion() != 1 {
		t.Fatal("a spec written before the stamp must read as schema 1")
	}
	saved := SaveAppSpec(AppSpec{Slug: "digest", Name: "Digest", Owner: "alice", Page: json.RawMessage(`{}`)})
	if saved.Schema != appSpecSchema {
		t.Fatalf("schema = %d, want %d", saved.Schema, appSpecSchema)
	}
}

// An import from an older schema upgrades to the current one; one from a
// newer schema is refused with the two numbers, not landed half-readable.
func TestAppSpecImportHonorsSchema(t *testing.T) {
	verifyTestStore(t)
	var art customAppArtifact

	older, _ := json.Marshal(AppSpec{Slug: "old", Name: "Old", Page: json.RawMessage(`{}`)}) // no stamp
	if _, skip, err := art.ImportArtifact(RootDB, older, "bob"); err != nil || skip != "" {
		t.Fatalf("older recipe should import: skip=%q err=%v", skip, err)
	}
	got, _ := LoadAppSpec("bob", "old")
	if got.Schema != appSpecSchema {
		t.Fatalf("imported spec not upgraded: schema %d", got.Schema)
	}

	newer, _ := json.Marshal(AppSpec{Slug: "new", Name: "New", Page: json.RawMessage(`{}`), Schema: appSpecSchema + 1})
	_, _, err := art.ImportArtifact(RootDB, newer, "bob")
	if err == nil || !strings.Contains(err.Error(), "newer gohort") {
		t.Fatalf("newer recipe should be refused by name: %v", err)
	}
	if _, exists := LoadAppSpec("bob", "new"); exists {
		t.Fatal("a refused recipe must not land")
	}
}

// The bundle envelope carries the writer's gohort version and the import
// result echoes it, so a report can say where the recipes came from.
func TestBundleCarriesGohortVersion(t *testing.T) {
	verifyTestStore(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	prevVer := AppVersion
	AppVersion = "0.6.696-test"
	t.Cleanup(func() { AppVersion = prevVer })

	SaveAppSpec(AppSpec{Slug: "digest", Name: "Digest", Owner: "alice", Page: json.RawMessage(`{}`), Notes: "for the nightly digest"})
	bundle, err := ExportArtifactBundleShallow(RootDB, []ArtifactSel{{Type: "custom_app", Name: "digest", Owner: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.GohortVersion != "0.6.696-test" {
		t.Fatalf("bundle version = %q", bundle.GohortVersion)
	}
	var spec AppSpec
	if err := json.Unmarshal(bundle.Artifacts[0].Recipe, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Notes != "for the nightly digest" || spec.Schema != appSpecSchema {
		t.Fatalf("recipe should carry notes and schema: %+v", spec)
	}
	data, _ := json.Marshal(bundle)
	AppVersion = "0.7.0-test" // a different install reading it
	res, err := ImportArtifactBundle(RootDB, data, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if res.GohortVersion != "0.6.696-test" || !strings.Contains(res.Summary(), "exported by gohort 0.6.696-test") {
		t.Fatalf("import result should name the writer: %+v / %q", res, res.Summary())
	}
}
