package core

// Published tools run from a release an administrator approved, not from the
// owner's working copy; a colleague's tool runs from the copy the taker froze.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

// releaseStore is a fresh tool store, wired as both RootDB and AuthDB (the
// tool approver publishes through AuthDB, as it does in production).
func releaseStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })
	return db
}

// publishedTool persists owner's tool and publishes it, as version 1.
func publishedTool(t *testing.T, db Database, owner string, tool TempTool) {
	t.Helper()
	if err := AdminPersistTempTool(db, owner, tool); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, owner, tool.Name, true); err != nil {
		t.Fatal(err)
	}
}

// editTool changes the owner's working copy of a tool's command.
func editTool(t *testing.T, db Database, owner, name, cmd string) {
	t.Helper()
	p, ok := UserToolByName(db, owner, name)
	if !ok {
		t.Fatalf("%s has no tool %s", owner, name)
	}
	p.Tool.CommandTemplate = cmd
	if err := AdminPersistTempTool(db, owner, p.Tool); err != nil {
		t.Fatal(err)
	}
}

// requestUpdate files the owner's update request, as the Extensions page does.
func requestUpdate(t *testing.T, db Database, owner, name string) string {
	t.Helper()
	if err := CreatePromotionRequest(db, owner, "tool", name, ""); err != nil {
		t.Fatal(err)
	}
	return PromotionRequestKey("tool", owner, name)
}

// adopted returns the one tool the user's agents load under name.
func adopted(t *testing.T, db Database, user, name string) LentTool {
	t.Helper()
	for _, p := range AdoptedToolsFor(db, user) {
		if p.Tool.Name == name {
			return p
		}
	}
	t.Fatalf("%s does not load %s", user, name)
	return LentTool{}
}

func releaseOf(t *testing.T, db Database, name string) ToolRelease {
	t.Helper()
	for _, r := range ToolReleases(db) {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no release of %s", name)
	return ToolRelease{}
}

// Update requested, reviewed against a diff, approved: version 2 reaches every
// adopter at once. And the version approved is the one the admin was shown,
// frozen when the owner asked, not whatever the working copy says by then.
func TestAnApprovedUpdateBecomesTheNextVersionForAdopters(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	editTool(t, db, "alice", "lookup", "v2-cmd")
	id := requestUpdate(t, db, "alice", "lookup")

	rel := releaseOf(t, db, "lookup")
	want, ok := rel.Requested(db)
	if !ok || want.CommandTemplate != "v2-cmd" {
		t.Fatalf("the review should show the requested definition: %+v %v", want, ok)
	}
	if d := rel.Tool.DefinitionDiff(want); !strings.Contains(d, "- v1-cmd") || !strings.Contains(d, "+ v2-cmd") {
		t.Fatalf("the review diff does not show the change:\n%s", d)
	}
	// The owner keeps editing after asking; the admin approves what was asked.
	editTool(t, db, "alice", "lookup", "v3-cmd-unreviewed")

	if got := adopted(t, db, "bob", "lookup"); got.Version != 1 || got.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("an update reached an adopter before approval: %+v", got)
	}
	if err := promotion.Approve(db, id, "admin"); err != nil {
		t.Fatal(err)
	}
	got := adopted(t, db, "bob", "lookup")
	if got.Version != 2 || got.Tool.CommandTemplate != "v2-cmd" {
		t.Fatalf("an approved update should reach adopters as version 2, got v%d %q", got.Version, got.Tool.CommandTemplate)
	}
	if rel := releaseOf(t, db, "lookup"); len(rel.History) != 1 || rel.History[0].Version != 1 {
		t.Fatalf("the replaced version should be kept: %+v", rel.History)
	}
}

// A request with nothing in it is refused, and a denied update changes nothing.
func TestADeniedUpdateLeavesTheReleaseAlone(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := CreatePromotionRequest(db, "alice", "tool", "lookup", ""); err == nil {
		t.Fatal("an update request with no change in it was filed")
	}
	editTool(t, db, "alice", "lookup", "v2-cmd")
	id := requestUpdate(t, db, "alice", "lookup")
	if err := SetPromotionRequestState(db, id, PromotionDeniedState, "admin"); err != nil {
		t.Fatal(err)
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 1 || rel.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("a denied update changed the release: v%d %q", rel.Version, rel.Tool.CommandTemplate)
	}
	if _, pending := rel.Requested(db); pending {
		t.Error("a denied request still reads as awaiting review")
	}
	// The admin's direct Share on an already-published tool is not a way
	// around the review either.
	if err := SetPersistentTempToolShared(db, "alice", "lookup", true); err != nil {
		t.Fatal(err)
	}
	if rel := releaseOf(t, db, "lookup"); rel.Version != 1 || rel.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("re-sharing a published tool published the working copy: v%d %q", rel.Version, rel.Tool.CommandTemplate)
	}
}

// Five replaced versions are kept, newest first, and any of them can be rolled
// back to; the rolled-back-from version is kept in turn, and numbering carries
// on past everything the release has held.
func TestReleaseHistoryIsCappedAndRollsBack(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "cmd-1"})
	for v := 2; v <= 8; v++ {
		editTool(t, db, "alice", "lookup", "cmd-"+string(rune('0'+v)))
		if err := promotion.Approve(db, requestUpdate(t, db, "alice", "lookup"), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 8 || len(rel.History) != toolReleaseHistoryCap {
		t.Fatalf("want v8 with %d kept, got v%d with %d", toolReleaseHistoryCap, rel.Version, len(rel.History))
	}
	for i, h := range rel.History {
		if h.Version != 7-i {
			t.Fatalf("history should be v7..v3 newest first, got %d at %d", h.Version, i)
		}
	}
	if err := rel.RollBack(db, 2, "admin"); err == nil {
		t.Error("rolled back to a version past the cap")
	}
	if err := rel.RollBack(db, 5, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	if got := adopted(t, db, "bob", "lookup"); got.Version != 5 || got.Tool.CommandTemplate != "cmd-5" {
		t.Fatalf("adopters should run the rolled-back version: v%d %q", got.Version, got.Tool.CommandTemplate)
	}
	rel = releaseOf(t, db, "lookup")
	if len(rel.History) != toolReleaseHistoryCap || rel.History[0].Version != 8 {
		t.Fatalf("the version rolled back from should be kept first: %+v", rel.History)
	}
	editTool(t, db, "alice", "lookup", "cmd-next")
	if err := promotion.Approve(db, requestUpdate(t, db, "alice", "lookup"), "admin"); err != nil {
		t.Fatal(err)
	}
	if rel := releaseOf(t, db, "lookup"); rel.Version != 9 {
		t.Fatalf("an update after a rollback reused a version number: v%d", rel.Version)
	}
}

// The owner may withdraw a published tool: it leaves the catalog, adopters
// stop loading it, and its last release is kept.
func TestAWithdrawnToolLeavesATombstone(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	id := releaseOf(t, db, "lookup").ID
	editTool(t, db, "alice", "lookup", "working-copy")
	if err := SetPersistentTempToolShared(db, "alice", "lookup", false); err != nil {
		t.Fatal(err)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("a withdrawn tool still loads for an adopter: %+v", got)
	}
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("a withdrawn tool is still in the catalog: %+v", got)
	}
	gone, ok := withdrawnToolRelease(db, id)
	if !ok || gone.Owner != "alice" || gone.Name != "lookup" || gone.Version != 1 ||
		gone.Tool.CommandTemplate != "v1-cmd" || gone.WithdrawnAt.IsZero() {
		t.Fatalf("the withdrawn release should be kept as it was: %+v %v", gone, ok)
	}
	// Deleting a published tool withdraws it the same way.
	publishedTool(t, db, "carol", TempTool{Name: "other", CommandTemplate: "c"})
	cid := releaseOf(t, db, "other").ID
	if err := DeletePersistentTempTool(db, "carol", "other"); err != nil {
		t.Fatal(err)
	}
	if _, ok := withdrawnToolRelease(db, cid); !ok {
		t.Error("deleting a published tool left no tombstone")
	}
}

// Upgrading gives every published tool its release, version 1 = what it is
// now, so nothing anybody runs changes; running it again changes nothing.
func TestTheReleaseMigrationIsIdempotent(t *testing.T) {
	db := releaseStore(t)
	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "lookup", CommandTemplate: "as-published"}, Shared: true},
		{Tool: TempTool{Name: "private"}},
	})
	// Before the migration there is no release: the catalog is empty.
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("a Shared row without a release is in the catalog: %+v", got)
	}
	if n := migrateToolReleases(db); n != 3 {
		t.Fatalf("want 2 IDs + 1 release, got %d changes", n)
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 1 || rel.Tool.CommandTemplate != "as-published" || rel.ID == "" {
		t.Fatalf("the migration should publish what the tool is now as version 1: %+v", rel)
	}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.ID == "" {
			t.Errorf("%s got no ID", p.Tool.Name)
		}
	}
	ids := map[string]string{}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		ids[p.Tool.Name] = p.ID
	}
	if n := migrateToolReleases(db); n != 0 {
		t.Errorf("a second run changed %d records", n)
	}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if ids[p.Tool.Name] != p.ID {
			t.Errorf("a second run re-minted %s's ID", p.Tool.Name)
		}
	}
	if again := releaseOf(t, db, "lookup"); again.ApprovedAt != rel.ApprovedAt || again.Version != 1 {
		t.Error("a second run rewrote the release")
	}
}

// A colleague's tool runs as the copy the taker froze. The colleague editing
// it offers an update the taker sees and may accept; until then they keep
// running what they took. Revoking the share still stops it.
func TestAPeerToolRunsTheFrozenCopyUntilAccepted(t *testing.T) {
	db := releaseStore(t)
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "ssh_run", CommandTemplate: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "bob", "ssh_run", "alice", true); err != nil {
		t.Fatal(err)
	}
	editTool(t, db, "alice", "ssh_run", "second")

	got := adopted(t, db, "bob", "ssh_run")
	if got.Tool.CommandTemplate != "first" {
		t.Fatalf("the colleague's edit reached the taker unaccepted: %q", got.Tool.CommandTemplate)
	}
	if got.Update == nil || got.Update.CommandTemplate != "second" {
		t.Fatalf("the taker should see an update available: %+v", got.Update)
	}
	if d := got.Tool.DefinitionDiff(*got.Update); !strings.Contains(d, "+ second") {
		t.Fatalf("the update's diff does not show the change:\n%s", d)
	}
	// Accept: take it again.
	if err := SetGlobalToolAdopted(db, "bob", "ssh_run", "alice", true); err != nil {
		t.Fatal(err)
	}
	got = adopted(t, db, "bob", "ssh_run")
	if got.Tool.CommandTemplate != "second" || got.Update != nil {
		t.Fatalf("accepting should switch to the current definition: %q %+v", got.Tool.CommandTemplate, got.Update)
	}
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run", nil); err != nil {
		t.Fatal(err)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("a revoked share still loads its frozen copy: %+v", got)
	}
}

// Adoptions written before records existed ("name<TAB>owner" and a bare name)
// still resolve, and gain an ID and, for a colleague's tool, the copy they
// were running, on first resolve.
func TestLegacyAdoptionEntriesStillResolve(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "published_one", CommandTemplate: "p"})
	if err := AdminPersistTempTool(db, "carol", TempTool{Name: "lent_one", CommandTemplate: "l1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(db, "carol", "lent_one", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	db.Set(adoptedGlobalToolsTable, "bob", []string{"lent_one\tcarol", "published_one"})

	got := AdoptedToolsFor(db, "bob")
	if len(got) != 2 {
		t.Fatalf("legacy adoptions should both resolve: %+v", got)
	}
	recs := loadAdoptions(db, "bob")
	if r := recs["published_one"]; r.Owner != "alice" || r.ID == "" || r.Copy != nil {
		t.Errorf("a legacy published adoption should be pinned to its owner and ID: %+v", r)
	}
	if r := recs["lent_one"]; r.ID == "" || r.Copy == nil || r.Copy.CommandTemplate != "l1" {
		t.Errorf("a legacy colleague adoption should freeze what it ran: %+v", r)
	}
	// The list stays readable in its own terms by anything that reads only it.
	if pins := loadAdoptionPins(db, "bob"); pins["published_one"] != "alice" || pins["lent_one"] != "carol" {
		t.Errorf("the adoption list lost its owners: %+v", pins)
	}
	editTool(t, db, "carol", "lent_one", "l2")
	if p := adopted(t, db, "bob", "lent_one"); p.Tool.CommandTemplate != "l1" || p.Update == nil {
		t.Fatalf("after its first resolve a legacy adoption is a frozen copy like any other: %+v", p)
	}
}

// A tool deleted and made again under the same name is a new tool: a new ID,
// and neither a publish nor a share of it reattaches an old adoption.
func TestARecreatedToolIsNotReattached(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "original"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	first := releaseOf(t, db, "lookup").ID
	if err := DeletePersistentTempTool(db, "alice", "lookup"); err != nil {
		t.Fatal(err)
	}
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "replacement"})
	if second := releaseOf(t, db, "lookup").ID; second == first || second == "" {
		t.Fatalf("a recreated tool kept the old ID %q", second)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("an adoption reattached to a recreated tool: %+v", got)
	}

	// The same for a colleague's share.
	if err := AdminPersistTempTool(db, "carol", TempTool{Name: "lent", CommandTemplate: "original"}); err != nil {
		t.Fatal(err)
	}
	_ = SetPersistentTempToolSharedWith(db, "carol", "lent", []string{"dana"})
	if err := SetGlobalToolAdopted(db, "dana", "lent", "carol", true); err != nil {
		t.Fatal(err)
	}
	_ = DeletePersistentTempTool(db, "carol", "lent")
	_ = AdminPersistTempTool(db, "carol", TempTool{Name: "lent", CommandTemplate: "replacement"})
	_ = SetPersistentTempToolSharedWith(db, "carol", "lent", []string{"dana"})
	if got := AdoptedToolsFor(db, "dana"); len(got) != 0 {
		t.Fatalf("an adoption reattached to a recreated shared tool: %+v", got)
	}
}

// The review diff names every changed field, and only changed ones; the
// governance flags are not changes.
func TestDefinitionDiffShowsChangedFieldsOnly(t *testing.T) {
	a := TempTool{Name: "t", Description: "old desc", CommandTemplate: "run {x}",
		ScriptBody: "line one\nline two\nline three", Params: map[string]ToolParam{"x": {Type: "string"}}}
	b := a
	b.Description = "new desc"
	b.ScriptBody = "line one\nline 2\nline three"
	b.Locked, b.Disabled = true, true
	d := a.DefinitionDiff(b)
	for _, want := range []string{"description:", "- old desc", "+ new desc", "script body:", "- line two", "+ line 2"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	for _, not := range []string{"line one", "locked", "disabled", "command"} {
		if strings.Contains(d, not) {
			t.Errorf("diff shows %q, which did not change:\n%s", not, d)
		}
	}
	if a.DefinitionDiff(a) != "" {
		t.Error("identical definitions produced a diff")
	}
	c := a
	c.Params = map[string]ToolParam{"x": {Type: "string"}, "y": {Type: "integer"}}
	if d := a.DefinitionDiff(c); !strings.Contains(d, "parameters:") || !strings.Contains(d, "+") {
		t.Errorf("a new parameter is not shown:\n%s", d)
	}
}

// A tool the user took and lost can be recreated as their own, from what they
// were running, when the owner withdrew or deleted it. Not when the owner took
// it away from this user in particular: that is a decision about them.
func TestALostToolIsRecreatedOnlyWhenItWasWithdrawn(t *testing.T) {
	setup := func(t *testing.T) Database {
		db := &DBase{Store: kvlite.MemStore()}
		saved := RootDB
		RootDB = db
		t.Cleanup(func() { RootDB = saved })
		if err := AdminPersistTempTool(db, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo alice-v1"}); err != nil {
			t.Fatal(err)
		}
		return db
	}
	own := func(db Database, user string) (TempTool, bool) {
		for _, p := range LoadPersistentTempTools(db, user) {
			if p.Tool.Name == "wiki_read" {
				return p.Tool, true
			}
		}
		return TempTool{}, false
	}

	t.Run("published then withdrawn", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", true)
		if err := SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true); err != nil {
			t.Fatal(err)
		}
		if _, err := RecreateLostTool(db, "bob", "wiki_read", false); err == nil {
			t.Fatal("a tool that still works was offered for recreation")
		}
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", false)
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err != nil {
			t.Fatalf("a withdrawn tool could not be recreated: %v", err)
		}
		got, ok := own(db, "bob")
		if !ok || got.CommandTemplate != "echo alice-v1" {
			t.Fatalf("bob's copy is not what he was running: %+v", got)
		}
		if LoadAdoptedGlobalTools(db, "bob")["wiki_read"] {
			t.Error("the adoption of the gone tool was left behind")
		}
	})
	t.Run("colleague deleted it", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", []string{"bob"})
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		AdoptedToolsFor(db, "bob") // takes the frozen copy
		_ = DeletePersistentTempTool(db, "alice", "wiki_read")
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err != nil {
			t.Fatalf("a deleted colleague's tool could not be recreated from the copy: %v", err)
		}
		if got, ok := own(db, "bob"); !ok || got.CommandTemplate != "echo alice-v1" {
			t.Fatalf("recreated from the wrong definition: %+v", got)
		}
	})
	t.Run("share revoked", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", []string{"bob"})
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		AdoptedToolsFor(db, "bob")
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", nil)
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err == nil {
			t.Error("recreating undid a revocation aimed at this user")
		}
		if _, ok := own(db, "bob"); ok {
			t.Error("a refused recreate still landed a tool")
		}
	})
	t.Run("left off the adopt list", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", true)
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		_ = SetPersistentTempToolAllowedUsers(db, "alice", "wiki_read", []string{"carol"})
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err == nil {
			t.Error("recreating undid an administrator narrowing the tool")
		}
	})
}
