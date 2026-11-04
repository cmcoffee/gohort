package filestore

// Deleting is the one thing this app does that cannot be undone, so the
// tests are about what it REFUSES to touch at least as much as what it
// removes.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// aged makes a directory with one file, both stamped `days` ago.
func aged(t *testing.T, root, name string, days int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(f, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -days)
	for _, p := range []string{f, dir} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func retentionFixture(t *testing.T, days int) (*FileStoreApp, string) {
	t.Helper()
	root := t.TempDir()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	if _, err := SaveStore(app.DB, Store{Name: "Bundles", Path: root, RetentionDays: days}); err != nil {
		t.Fatal(err)
	}
	return app, root
}

func TestRetentionRemovesOnlyExpiredFolders(t *testing.T) {
	app, root := retentionFixture(t, 30)
	old := aged(t, root, "scan-old", 90)
	fresh := aged(t, root, "scan-fresh", 2)
	// A loose file at the root is never a candidate: only directories.
	loose := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(loose, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -400)
	_ = os.Chtimes(loose, when, when)

	list := FindExpiredBundles(context.Background(), app.DB)
	if len(list) != 1 || list[0].Folder != "scan-old" {
		t.Fatalf("expected only the old folder, got %+v", list)
	}
	if gone, _ := ReapExpiredBundles(context.Background(), app.DB); gone != 1 {
		t.Fatalf("expected one removal, got %d", gone)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the expired folder survived")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh folder was removed")
	}
	if _, err := os.Stat(loose); err != nil {
		t.Error("a loose file at the store root was removed — only directories are candidates")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the store ROOT was removed")
	}
}

// The default is the one that matters: a store nobody configured must
// never yield a candidate, however old its contents are.
func TestNoWindowMeansNothingIsEverEligible(t *testing.T) {
	app, root := retentionFixture(t, 0)
	aged(t, root, "ancient", 4000)
	if list := FindExpiredBundles(context.Background(), app.DB); len(list) != 0 {
		t.Fatalf("a store with no retention window produced candidates: %+v", list)
	}
	if gone, _ := ReapExpiredBundles(context.Background(), app.DB); gone != 0 {
		t.Fatal("something was deleted from a store with no window")
	}
}

// A symlinked folder pointing out of the store must not become a delete
// target just because it is listed inside it.
func TestRetentionRefusesToFollowASymlinkOut(t *testing.T) {
	app, root := retentionFixture(t, 1)
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep-me")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	when := time.Now().AddDate(0, 0, -900)
	_ = os.Chtimes(victim, when, when)
	if err := os.Symlink(victim, filepath.Join(root, "sneaky")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ReapExpiredBundles(context.Background(), app.DB)
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a directory OUTSIDE the store was deleted through a symlink")
	}
}

// The dry run and the delete must describe the same set — an admin
// approves what they read.
func TestDryRunTextMatchesWhatWouldGo(t *testing.T) {
	app, root := retentionFixture(t, 30)
	aged(t, root, "scan-old", 90)
	text := FormatExpiredBundles(FindExpiredBundles(context.Background(), app.DB))
	if !strings.Contains(text, "scan-old") || !strings.Contains(text, "1 folder(s)") {
		t.Errorf("the report should name the folder and the count: %s", text)
	}
	if empty := FormatExpiredBundles(nil); !strings.Contains(empty, "nothing eligible") {
		t.Errorf("an empty report should say so plainly: %s", empty)
	}
}

// Nobody reaches a store until an admin names them; Read-Only reads, Full
// Access changes things; a team and Everyone are entries like a person; and
// being an admin is no entry.
func TestWhoReachesAStore(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev; DeleteAppGroup(adb, "g-ops") })
	SaveAppGroup(adb, AppGroup{ID: "g-ops", Name: "Ops", Shareable: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Groups: []string{"g-ops"}})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})

	if (Store{}).AllowsUser("root") {
		t.Error("an empty list let somebody in, an admin included")
	}
	st := Store{Members: []string{"alice", "group:g-ops"}, FullAccess: []string{"alice"}}
	for user, want := range map[string]string{"alice": accessFull, "bob": accessRead, "carol": "", "root": ""} {
		if got := st.accessFor(user); got != want {
			t.Errorf("%s: %q, want %q", user, got, want)
		}
	}
	if st.FullAccessFor("bob") {
		t.Error("Read-Only through a team changed things")
	}
	everyone := Store{Members: []string{accessEveryone, "alice"}, FullAccess: []string{"alice"}}
	if !everyone.AllowsUser("carol") || everyone.FullAccessFor("carol") || !everyone.FullAccessFor("alice") {
		t.Error("Everyone at Read-Only, with alice raised above it, came out wrong")
	}
}

// Each member once, and Full Access only for members: removing someone takes
// their level with them.
func TestAStoresAccessListsAreTidied(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Drop", Path: t.TempDir(),
		Members: []string{" alice ", "Alice", "", "bob"}, FullAccess: []string{"alice", "carol"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Members, ",") != "alice,bob" || strings.Join(st.FullAccess, ",") != "alice" {
		t.Errorf("members %v, full access %v", st.Members, st.FullAccess)
	}
}

// A stopped reap removes nothing, and a stopped dry run lists nothing past
// where it stopped.
func TestAStoppedBundleReapRemovesNothing(t *testing.T) {
	app, root := retentionFixture(t, 30)
	stale := aged(t, root, "old-bundle", 90)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if list := FindExpiredBundles(stopped, app.DB); len(list) != 0 {
		t.Fatalf("a stopped dry run listed %d folder(s)", len(list))
	}
	if gone, _ := ReapExpiredBundles(stopped, app.DB); gone != 0 {
		t.Fatalf("a stopped reap removed %d folder(s)", gone)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a stopped reap removed the folder")
	}
}

// runPass runs fn as a maintenance pass under a key of its own and returns how
// core reported it ending: the same path the timers take, so what is checked
// is what the admin records.
func runPass(t *testing.T, key string, fn func(ctx context.Context) int) MaintenanceEnded {
	t.Helper()
	saved := OnMaintenanceEnded
	t.Cleanup(func() { OnMaintenanceEnded = saved })
	var got []MaintenanceEnded
	OnMaintenanceEnded = func(e MaintenanceEnded) {
		if e.Key == key {
			got = append(got, e)
		}
	}
	RegisterMaintenanceFunc("Housekeeping", key, key, "test pass", fn)
	RunMaintenanceFunc(context.Background(), key)
	if len(got) != 1 {
		t.Fatalf("the pass was reported %d times, want once", len(got))
	}
	return got[0]
}

// The two timed sweeps are maintenance passes, so each run is recorded and
// either can be run now from Housekeeping.
func TestTheTimedSweepsAreMaintenancePasses(t *testing.T) {
	want := map[string]bool{temporarySweepKey: false, evidenceSweepKey: false}
	for _, f := range ListMaintenanceFuncs() {
		if _, ok := want[f.Key]; ok {
			want[f.Key] = true
			if f.Group != "Housekeeping" {
				t.Errorf("%s is under %q, want Housekeeping", f.Key, f.Group)
			}
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("%s is not a registered maintenance pass, so its runs are not recorded", k)
		}
	}
}

// A temporary sweep says what it removed.
func TestATemporarySweepReportsWhatItRemoved(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	if _, err := SaveStore(app.DB, Store{Name: "Intake", Path: root, Ephemeral: true, LifetimeHours: 1}); err != nil {
		t.Fatal(err)
	}
	aged(t, root, "CASE-1", 1)
	end := runPass(t, "test_temporary_sweep_reports", func(ctx context.Context) int { return sweepTemporaryPass(ctx, app.DB) })
	if end.Failed || end.Changed != 1 || !strings.HasPrefix(end.Outcome, "removed 1 expired folder(s)") {
		t.Errorf("the sweep ended as %+v", end)
	}
}

// A folder this server cannot remove whole is never tried: it is left
// exactly as it was, not emptied of what could be deleted, and the run fails
// naming it, so the cleanup that cannot happen is on System Status.
func TestAFolderItCannotRemoveIsLeftWhole(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes a folder from a read-only parent, so the case cannot be made")
	}
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	if _, err := SaveStore(app.DB, Store{Name: "Intake", Path: root, Ephemeral: true, LifetimeHours: 1}); err != nil {
		t.Fatal(err)
	}
	stuck := aged(t, root, "CASE-1", 1)
	inner := filepath.Join(stuck, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(inner, "x"), []byte("x"), 0o644)
	// The folder's own contents cannot be unlinked, so it cannot go whole.
	if err := os.Chmod(inner, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(inner, 0o755) })
	when := time.Now().AddDate(0, 0, -1)
	os.Chtimes(stuck, when, when)
	os.Chtimes(inner, when, when)
	gone := aged(t, root, "CASE-2", 1) // a removable neighbour still goes

	end := runPass(t, "test_temporary_sweep_blocked", func(ctx context.Context) int { return sweepTemporaryPass(ctx, app.DB) })
	if !end.Failed || !strings.Contains(end.Outcome, "left alone") || !strings.Contains(end.Outcome, "Intake / CASE-1") || end.Changed != 1 {
		t.Errorf("the run ended as %+v", end)
	}
	// Whole: the file at its top, which a RemoveAll would have taken first,
	// is still there.
	if _, err := os.Stat(filepath.Join(stuck, "log.txt")); err != nil {
		t.Errorf("the folder it could not remove was partly emptied: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("the removable neighbour should have gone: %v", err)
	}
}

// A store whose folder this server cannot delete in is not read for cleanup
// at all: listed once as left alone, nothing tried.
func TestAStoreItCannotDeleteInIsNotTried(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can delete in a read-only folder")
	}
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	root := t.TempDir()
	st, err := SaveStore(app.DB, Store{Name: "Build Logs", Path: root, Ephemeral: true, LifetimeHours: 1})
	if err != nil {
		t.Fatal(err)
	}
	old := aged(t, root, "anaconda", 30)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })

	sc := findExpired(context.Background(), app.DB, true)
	if len(sc.bundles) != 0 || len(sc.blocked) != 1 || !strings.HasPrefix(sc.blocked[0], "Build Logs: ") {
		t.Errorf("scan = %+v, want the store left alone once", sc)
	}
	end := runPass(t, "test_temporary_sweep_unwritable_root", func(ctx context.Context) int { return sweepTemporaryPass(ctx, app.DB) })
	if !end.Failed || !strings.Contains(end.Outcome, "no permission to delete in "+root) {
		t.Errorf("the run ended as %+v", end)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("a folder in an undeletable store went: %v", err)
	}
	// The row says so, short in the table and in full in the details.
	if k := keepsLabel(st); !strings.Contains(k, "can't delete here") {
		t.Errorf("table row = %q", k)
	}
	if r := retentionLabel(st); !strings.Contains(r, "NOTHING IS DELETED") {
		t.Errorf("details = %q", r)
	}
}

// Cleanup cannot be turned ON for a folder this server cannot delete in, and a
// store that already cleans up stays editable.
func TestCleanupIsRefusedWhereItCannotDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can delete in a read-only folder")
	}
	db := &DBase{Store: kvlite.MemStore()}
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := SaveStore(db, Store{Name: "Logs", Path: locked, Ephemeral: true}); err == nil || !strings.Contains(err.Error(), "Temporary deletes folders from") {
		t.Errorf("temporary on an undeletable folder: %v", err)
	}
	if _, err := SaveStore(db, Store{Name: "Logs", Path: locked, RetentionDays: 30}); err == nil || !strings.Contains(err.Error(), "a retention window deletes") {
		t.Errorf("retention on an undeletable folder: %v", err)
	}
	// A read-only store is fine there: it deletes nothing.
	if _, err := SaveStore(db, Store{Name: "Logs", Path: locked}); err != nil {
		t.Fatalf("a store that deletes nothing was refused: %v", err)
	}

	// One that already cleans up, whose folder lost write access later, still
	// saves: the edit is not about cleanup, and the row says what is wrong.
	open := t.TempDir()
	tmp, err := SaveStore(db, Store{Name: "Intake", Path: open, Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(open, 0o555)
	t.Cleanup(func() { os.Chmod(open, 0o755) })
	tmp.Description = "raw downloads"
	if _, err := SaveStore(db, tmp); err != nil {
		t.Errorf("an existing temporary store could not be edited: %v", err)
	}
}
