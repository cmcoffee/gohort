package filestore

// Deleting is the one thing this app does that cannot be undone, so the
// tests are about what it REFUSES to touch at least as much as what it
// removes.

import (
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

	list := FindExpiredBundles(app.DB)
	if len(list) != 1 || list[0].Folder != "scan-old" {
		t.Fatalf("expected only the old folder, got %+v", list)
	}
	if gone, _ := ReapExpiredBundles(app.DB); gone != 1 {
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
	if list := FindExpiredBundles(app.DB); len(list) != 0 {
		t.Fatalf("a store with no retention window produced candidates: %+v", list)
	}
	if gone, _ := ReapExpiredBundles(app.DB); gone != 0 {
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
	ReapExpiredBundles(app.DB)
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a directory OUTSIDE the store was deleted through a symlink")
	}
}

// The dry run and the delete must describe the same set — an admin
// approves what they read.
func TestDryRunTextMatchesWhatWouldGo(t *testing.T) {
	app, root := retentionFixture(t, 30)
	aged(t, root, "scan-old", 90)
	text := FormatExpiredBundles(FindExpiredBundles(app.DB))
	if !strings.Contains(text, "scan-old") || !strings.Contains(text, "1 folder(s)") {
		t.Errorf("the report should name the folder and the count: %s", text)
	}
	if empty := FormatExpiredBundles(nil); !strings.Contains(empty, "nothing eligible") {
		t.Errorf("an empty report should say so plainly: %s", empty)
	}
}

// Uploading is a separate grant from reaching the store.
func TestUploadGate(t *testing.T) {
	open := Store{}
	if !open.UploadsAllowedFor("anyone", true) {
		t.Error("an admin should be able to upload to an unrestricted store")
	}
	if open.UploadsAllowedFor("anyone", false) {
		t.Error("a non-admin must not upload until uploads are turned on")
	}
	drop := Store{AllowUploads: true}
	if !drop.UploadsAllowedFor("anyone", false) {
		t.Error("a drop store should accept an assigned user's upload")
	}
	// Assignment still bounds it — turning uploads on does not widen WHO.
	restricted := Store{AllowUploads: true, AllowedUsers: []string{"alice"}}
	if restricted.UploadsAllowedFor("bob", false) {
		t.Error("uploads=on must not let an unassigned user in")
	}
	if restricted.UploadsAllowedFor("bob", true) {
		t.Error("not even an admin: membership decides reach, and reach gates upload")
	}
}
