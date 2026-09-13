package core

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func verifyTestStore(t *testing.T) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prev })
}

// A verdict is pinned to the revision it checked. Storing it must not bump
// Updated (or it would be stale the instant it landed), and any later edit
// must make it read as stale without anyone clearing it.
func TestAppVerifyStatePinsToTheRevisionItChecked(t *testing.T) {
	verifyTestStore(t)
	spec := SaveAppSpec(AppSpec{Slug: "digest", Name: "Digest", Owner: "alice", Page: json.RawMessage(`{"a":1}`)})
	if got := spec.VerifyStatus(); got == "" || spec.Verify != nil {
		t.Fatalf("fresh spec should read never-verified: %q", got)
	}
	if !spec.RecordVerify(true, "PASS") {
		t.Fatal("verify state not stored")
	}
	got, _ := LoadAppSpec("alice", "digest")
	if got.Updated != spec.Updated {
		t.Fatalf("storing a verdict changed Updated: %s → %s", spec.Updated, got.Updated)
	}
	if !got.Verify.Current(got) || !got.Verify.Pass {
		t.Fatalf("verdict should be current and passing: %+v", got.Verify)
	}
	// An edit produces a new revision; the verdict now describes an old one.
	got.Page = json.RawMessage(`{"a":2}`)
	got.Updated = "2001-01-01T00:00:00Z" // force a visible change even inside one second
	edited := SaveAppSpecAs(got, "update")
	if edited.Verify == nil {
		t.Fatal("the verdict should survive the edit (it is history), just as stale")
	}
	if edited.Verify.Current(edited) && edited.Updated != spec.Updated {
		t.Fatalf("verdict still reads current after an edit: against=%s updated=%s", edited.Verify.Against, edited.Updated)
	}
}

// Export and import both drop the verdict: a browser load on one host says
// nothing about the host the recipe lands on.
func TestAppVerifyStateDoesNotTravel(t *testing.T) {
	verifyTestStore(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	spec := SaveAppSpec(AppSpec{Slug: "digest", Name: "Digest", Owner: "alice", Page: json.RawMessage(`{"a":1}`), ChangeNote: "first cut"})
	spec.RecordVerify(true, "PASS")

	var art customAppArtifact
	recipe, err := art.ExportArtifact(RootDB, "digest", "alice")
	if err != nil {
		t.Fatal(err)
	}
	var exported AppSpec
	if err := json.Unmarshal(recipe, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Verify != nil {
		t.Fatalf("export carried a verdict: %+v", exported.Verify)
	}
	if exported.ChangeNote != "first cut" {
		t.Fatalf("the change note is part of the app's shape and should travel: %q", exported.ChangeNote)
	}
	// A hand-edited bundle that smuggles one in is dropped on import too.
	exported.Verify = &AppVerifyState{Against: "x", Pass: true}
	tampered, _ := json.Marshal(exported)
	if _, _, err := art.ImportArtifact(RootDB, tampered, "bob"); err != nil {
		t.Fatal(err)
	}
	imported, ok := LoadAppSpec("bob", "digest")
	if !ok {
		t.Fatal("import did not land")
	}
	if imported.Verify != nil {
		t.Fatalf("import kept a foreign verdict: %+v", imported.Verify)
	}
	if !imported.Disabled {
		t.Fatal("import should land disabled")
	}
}

// A sample is a test fixture, not a revision: storing it leaves Updated and
// the history alone, and nil clears it.
func TestRecordSampleDoesNotBumpTheRevision(t *testing.T) {
	verifyTestStore(t)
	spec := SaveAppSpec(AppSpec{Slug: "digest", Name: "Digest", Owner: "alice", Page: json.RawMessage(`{"a":1}`)})
	if !spec.RecordSample([]map[string]any{{"city": "Santa Cruz"}}) {
		t.Fatal("sample not stored")
	}
	got, _ := LoadAppSpec("alice", "digest")
	if got.Updated != spec.Updated || len(got.Sample) != 1 || got.Sample[0]["city"] != "Santa Cruz" {
		t.Fatalf("sample/Updated wrong: %+v", got)
	}
	if len(ListAppRevisions("alice", "digest")) != 0 {
		t.Fatal("a sample must not file a revision")
	}
	spec.RecordSample(nil)
	if got, _ := LoadAppSpec("alice", "digest"); len(got.Sample) != 0 {
		t.Fatal("nil should clear the sample")
	}
}
