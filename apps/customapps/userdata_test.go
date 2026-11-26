package customapps

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appadmin"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A deleted account's apps move to someone else with their records and their
// sharing, or are deleted; nothing is left filed under the old name.
func TestADeletedAccountsAppsMoveOrGo(t *testing.T) {
	T := sharingTestApp(t)
	h := &appsUserData{app: T}
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "bob", RecordKey: "id", SharedCollections: []string{"board"}})
	SaveAppSpec(AppSpec{Slug: "notes", Name: "Notes", Owner: "bob", RecordKey: "id"})
	SaveAppSpec(AppSpec{Slug: "notes", Name: "Alice's own notes", Owner: "alice", RecordKey: "id"})
	if err := T.setShared("bob", "tally", true); err != nil {
		t.Fatal(err)
	}
	bobBase := T.recordBase(AppSpec{Slug: "tally", Owner: "bob"}, "bob")
	bobBase.Set(recTable("tally"), "r1", map[string]any{"id": "r1", "n": 3})
	bobBase.Set(sharedTable("tally", "board"), "s1", map[string]any{"id": "s1", "by": "carol"})

	if s := h.Describe("bob"); s.Counts["apps"] != 2 || s.Counts["shared"] != 1 {
		t.Fatalf("describe: %+v", s.Counts)
	}
	if err := h.Reassign("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(listSpecs("bob")) != 0 {
		t.Error("bob still owns apps after they moved")
	}
	moved, ok := loadSpec("alice", "tally")
	if !ok || moved.Owner != "alice" || !moved.Shared {
		t.Fatalf("tally did not arrive as alice's shared app: %+v %v", moved, ok)
	}
	var rec map[string]any
	if !T.recordBase(moved, "alice").Get(recTable("tally"), "r1", &rec) {
		t.Error("bob's records did not travel with the app")
	}
	if !T.recordBase(moved, "alice").Get(sharedTable("tally", "board"), "s1", &rec) {
		t.Error("the app's shared collection, everyone's entries, stayed with bob")
	}
	if owner, _ := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); owner != "alice" {
		t.Errorf("the shared index still names %q", owner)
	}
	// Alice had her own "notes": bob's arrives beside it, hers untouched.
	if own, _ := loadSpec("alice", "notes"); own.Name != "Alice's own notes" {
		t.Error("the move overwrote the new owner's own app")
	}
	if got, ok := loadSpec("alice", "notes-2"); !ok || got.Name != "Notes" {
		t.Error("bob's notes should arrive under a free slug")
	}

	// Purge: carol's apps go, sharing included.
	SaveAppSpec(AppSpec{Slug: "poll", Name: "Poll", Owner: "carol"})
	T.setShared("carol", "poll", true)
	if err := h.Purge("carol"); err != nil {
		t.Fatal(err)
	}
	if len(listSpecs("carol")) != 0 {
		t.Error("purge left carol's apps")
	}
	if _, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "poll"); shared {
		t.Error("purge left the app in the shared index")
	}
}

// A shared app an operator narrowed to a deleted account keeps a marker, so
// it does not fall open to every signed-in user.
func TestADeletedAccountLeavesAnAppsAudience(t *testing.T) {
	sharingTestApp(t)
	appadmin.Save(RootDB, "alice", "tally", appadmin.State{AllowedUsers: []string{"bob"}})
	appadmin.Save(RootDB, "alice", "poll", appadmin.State{AllowedUsers: []string{"bob", "carol"}})
	forgetAppReader("bob")
	if st := appadmin.Load(RootDB, "alice", "tally"); len(st.AllowedUsers) != 1 || st.AllowedUsers[0] == "bob" {
		t.Errorf("an audience of only bob: %v", st.AllowedUsers)
	}
	if st := appadmin.Load(RootDB, "alice", "poll"); len(st.AllowedUsers) != 1 || st.AllowedUsers[0] != "carol" {
		t.Errorf("poll's audience: %v", st.AllowedUsers)
	}
}

// An app on its own file keeps it when it moves to another account, records
// and all, and deleting the app then removes the file from disk.
func TestAnAppsOwnFileMovesWithItAndGoesWhenItIsDeleted(t *testing.T) {
	T := sharingTestApp(t)
	savedAuth := AuthDB
	adb := &DBase{Store: kvlite.MemStore()}
	for _, u := range []string{"alice", "bob"} {
		adb.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	AuthDB = func() Database { return adb }
	dir := t.TempDir()
	file := func(name string) string { return filepath.Join(dir, "gohort_"+name+".db") }
	SetPrivateDBOpener(func(name string) (Database, error) {
		st, err := kvlite.Open(file(name))
		if err != nil {
			return nil, err
		}
		return &DBase{Store: st}, nil
	}, func(name string) error { return os.Remove(file(name)) })
	t.Cleanup(func() { AuthDB = savedAuth; SetPrivateDBOpener(nil, nil) })

	spec := SaveAppSpec(AppSpec{Slug: "ledger", Name: "Ledger", Owner: "bob", RecordKey: "id", PrivateDB: true})
	T.recordBase(spec, "bob").Set(recTable("ledger"), "r1", map[string]any{"id": "r1"})
	onDisk := file("customapp_ledger_" + hashOf(t, "bob"))
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("the app's own file is not on disk: %v", err)
	}

	if err := (&appsUserData{app: T}).Reassign("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	moved, ok := loadSpec("alice", "ledger")
	if !ok {
		t.Fatal("the app did not move")
	}
	var rec map[string]any
	if !T.recordBase(moved, "alice").Get(recTable("ledger"), "r1", &rec) {
		t.Fatal("the moved app lost its records")
	}
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("the move deleted the file the moved app uses: %v", err)
	}

	T.deleteApp("alice", "ledger")
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("deleting the app left its file: %v", err)
	}
}

// hashOf is the short owner hash core puts in a per-app file's name.
func hashOf(t *testing.T, owner string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:6])
}
