package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// app.json written by a model quotes min and max as often as not; both
// spellings read as the number, and a non-number is refused by name.
func TestAppSettingBoundsReadQuotedOrNot(t *testing.T) {
	var s AppSetting
	if err := json.Unmarshal([]byte(`{"name":"n","default":"10","min":"1","max":50}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "n" || s.Default != "10" || s.Min != 1 || s.Max != 50 {
		t.Fatalf("read %+v", s)
	}
	if err := json.Unmarshal([]byte(`{"name":"n","min":"ten"}`), &s); err == nil || !strings.Contains(err.Error(), "min") {
		t.Fatalf("a non-number min: err=%v", err)
	}
}

func TestScheduleFromSetting(t *testing.T) {
	for _, tc := range []struct {
		in   string
		secs int
		cron string
		bad  bool
	}{
		{in: "06:00", cron: "daily 06:00"},
		{in: " 7:30 ", cron: "daily 7:30"},
		{in: "MON,FRI 09:00", cron: "MON,FRI 09:00"},
		{in: "weekdays 08:15", cron: "weekdays 08:15"},
		{in: "30", secs: 1800},
		{in: "2", secs: MinAppScheduleSeconds},
		{in: "0", bad: true},
		{in: "", bad: true},
		{in: "breakfast", bad: true},
		{in: "25:00", bad: true},
	} {
		secs, cron, err := ScheduleFromSetting(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q should not read as a time, got %d %q", tc.in, secs, cron)
			}
			continue
		}
		if err != nil || secs != tc.secs || cron != tc.cron {
			t.Errorf("%q = %d %q %v, want %d %q", tc.in, secs, cron, err, tc.secs, tc.cron)
		}
	}
}

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

// A shared app's approval covers what it was: a non-admin owner changing its
// page or scripts takes it out of sharing until an admin approves again.

func TestEditingASharedAppUnsharesItForANonAdmin(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })

	for _, owner := range []string{"alice", "root"} {
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Page: json.RawMessage(`{"a":1}`), Shared: true})
		// A metadata edit keeps it shared.
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Name: "Renamed", Page: json.RawMessage(`{"a":1}`), Shared: true})
		if s, _ := LoadAppSpec(owner, "tally"); !s.Shared {
			t.Fatalf("%s: a rename unshared the app", owner)
		}
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Page: json.RawMessage(`{"a":2,"script":"steal()"}`), Shared: true})
		s, _ := LoadAppSpec(owner, "tally")
		if owner == "alice" && s.Shared {
			t.Error("a non-admin's page change kept the app shared")
		}
		if owner == "root" && !s.Shared {
			t.Error("an admin's own edit unshared the app")
		}
	}

	// A library every script imports is what the app runs too.
	lib := func(code string) AppSpec {
		return AppSpec{Slug: "lib", Owner: "alice", Page: json.RawMessage(`{"a":1}`), Shared: true,
			Libraries: map[string]string{"engine": code}}
	}
	SaveAppSpec(lib("def turn(s): return s"))
	SaveAppSpec(lib("def turn(s): return steal(s)"))
	if s, _ := LoadAppSpec("alice", "lib"); s.Shared {
		t.Error("swapping a library kept the app shared")
	}
}

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

// A save that changes only words about the app keeps its verdict current; the
// create result asks for notes, and that write used to read back unverified.
func TestAppVerdictCarriesAcrossANotesEdit(t *testing.T) {
	verifyTestStore(t)
	spec := SaveAppSpec(AppSpec{Slug: "game", Name: "Game", Owner: "alice", Sections: json.RawMessage(`[{"kind":"html"}]`)})
	// Stamp the verified revision in the past, so a verdict left behind reads
	// stale instead of matching a save in the same second.
	old := "2001-01-01T00:00:00Z"
	spec.Updated = old
	spec.Verify = &AppVerifyState{At: old, Against: old, Pass: true, Summary: "PASS"}
	appSpecStore("alice").Set(AppSpecTable, spec.Slug, spec)

	got, _ := LoadAppSpec("alice", "game")
	got.Notes, got.Desc = "plan: swipe, undo", "a tile game"
	noted := SaveAppSpecAs(got, "update")
	if noted.Updated == old || !noted.Verify.Current(noted) || !noted.Verify.Pass {
		t.Fatalf("a notes edit dropped the verdict: %+v vs %s", noted.Verify, noted.Updated)
	}

	appSpecStore("alice").Set(AppSpecTable, spec.Slug, spec)
	got, _ = LoadAppSpec("alice", "game")
	got.Settings = []AppSetting{{Name: "SIZE", Default: "4"}}
	tuned := SaveAppSpecAs(got, "update")
	if tuned.Verify.Current(tuned) {
		t.Fatalf("a settings change kept the verdict current: %+v", tuned.Verify)
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

// An imported app has the record key every app made here has: one whose
// recipe names none gets "id", and one that names its own keeps it.
func TestAnImportedAppHasARecordKey(t *testing.T) {
	if got := importedAppSpec(AppSpec{Slug: "books"}, "v").RecordKey; got != "id" {
		t.Errorf("no key in the recipe: %q, want id", got)
	}
	if got := importedAppSpec(AppSpec{Slug: "books", RecordKey: "isbn"}, "v").RecordKey; got != "isbn" {
		t.Errorf("the recipe's own key: %q, want isbn", got)
	}
}

// Offering an app over MCP to the people it is shared with is how this copy
// runs here: turning it on is not a change to what the app serves (so a
// shared app stays shared), and it never travels in an export or arrives
// with an import.
func TestOfferingAnAppOverMCPIsLocal(t *testing.T) {
	prior := AppSpec{Slug: "books", Owner: "u", Shared: true}
	next := prior
	next.OfferMCPToShared = true
	if appServesDifferently(prior, next) {
		t.Error("offering the app over MCP reads as a change to what it serves")
	}
	if importedAppSpec(next, "v").OfferMCPToShared {
		t.Error("an imported app arrives offered over MCP")
	}
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	SaveAppSpec(AppSpec{Slug: "books", Name: "Books", Owner: "u", OfferMCPToShared: true})
	raw, err := customAppArtifact{}.ExportArtifact(nil, "books", "u")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "offer_mcp_to_shared") {
		t.Errorf("the export carries the offer: %s", raw)
	}
}

// A new app gets its own database file unless another app already keeps its
// records in the file that name would open; deleting one removes its file,
// closing the cached handle first, only when no other app uses it.
func TestAnAppsOwnFileIsMadeAndRemovedOnlyWhenNoOtherAppUsesIt(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	AuthDB = func() Database { return adb }
	opened := map[string]int{}
	var removed []string
	SetPrivateDBOpener(func(name string) (Database, error) {
		opened[name]++
		return &DBase{Store: kvlite.MemStore()}, nil
	}, func(name string) error {
		removed = append(removed, name)
		return nil
	})
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth; SetPrivateDBOpener(nil, nil) })

	if s := (AppSpec{Owner: "alice", Slug: "ledger"}).OwnFileByDefault(); !s.PrivateDB {
		t.Fatal("a new app did not get its own file")
	}
	// alice's notes moved to bob keep the file named for alice.
	SaveAppSpec(AppSpec{Owner: "bob", Slug: "notes", PrivateDB: true, DataOwner: "alice"})
	if s := (AppSpec{Owner: "alice", Slug: "notes"}).OwnFileByDefault(); s.PrivateDB {
		t.Fatal("a new app was put on a file another app keeps its records in")
	}
	for _, none := range []func() Database{nil, func() Database { return nil }} {
		AuthDB = none
		if s := (AppSpec{Owner: "alice", Slug: "ledger"}).OwnFileByDefault(); s.PrivateDB {
			t.Fatal("with no account list to check, a new app was put on a file anyway")
		}
	}
	AuthDB = func() Database { return adb }

	// A file another app uses stays.
	if err := (AppSpec{Owner: "alice", Slug: "notes", PrivateDB: true}).RemovePrivateDB(); err != nil || len(removed) != 0 {
		t.Fatalf("removed %v (%v): another app still uses it", removed, err)
	}
	// An app in the shared store has no file to remove.
	if (AppSpec{Owner: "alice", Slug: "tally"}).RemovePrivateDB(); len(removed) != 0 {
		t.Fatalf("removed %v for an app in the shared store", removed)
	}
	// An app still stored keeps its file.
	SaveAppSpec(AppSpec{Owner: "alice", Slug: "kept", PrivateDB: true})
	if (AppSpec{Owner: "alice", Slug: "kept", PrivateDB: true}).RemovePrivateDB(); len(removed) != 0 {
		t.Fatalf("removed %v while the app is still stored", removed)
	}
	// An app in the shared store under the same name does not use the file:
	// bob's own "sheet" beside the one he moved to alice.
	SaveAppSpec(AppSpec{Owner: "bob", Slug: "sheet"})
	sheet := AppSpec{Owner: "alice", Slug: "sheet", PrivateDB: true, DataOwner: "bob"}
	if err := sheet.RemovePrivateDB(); err != nil || len(removed) != 1 || removed[0] != customAppDBName("bob", "sheet") {
		t.Fatalf("removed %v (%v): an app in the shared store kept the file", removed, err)
	}
	removed = nil
	// One nobody else uses goes, and a later open makes a fresh one.
	ledger := AppSpec{Owner: "alice", Slug: "ledger", PrivateDB: true}
	name := customAppDBName("alice", "ledger")
	OpenCustomAppDB("alice", "ledger").Set("t", "k", "v")
	if err := ledger.RemovePrivateDB(); err != nil || len(removed) != 1 || removed[0] != name {
		t.Fatalf("removed %v (%v), want %s", removed, err, name)
	}
	var v string
	if OpenCustomAppDB("alice", "ledger").Get("t", "k", &v) || opened[name] != 2 {
		t.Fatalf("the removed file's handle was kept: opens %d, read %q", opened[name], v)
	}
}
