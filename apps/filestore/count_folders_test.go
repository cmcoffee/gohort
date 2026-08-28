package filestore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCountFoldersAgreesWithListFolders — the two answer the same question
// about what a folder IS, and a picker that disagreed with the menu it opens
// would read as a bug in whichever the reader looked at second.
func TestCountFoldersAgreesWithListFolders(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		d := filepath.Join(root, fmt.Sprintf("bundle-%02d", i))
		if err := os.MkdirAll(filepath.Join(d, "logs"), 0o755); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 5; j++ {
			if err := os.WriteFile(filepath.Join(d, "logs", fmt.Sprintf("f%d.log", j)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A loose file at the root is not a folder, in either.
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nor is a symlink to one: ListFolders keys on DirEntry.IsDir, which is
	// false for a link, and the count has to key on the same thing.
	if err := os.Symlink(filepath.Join(root, "bundle-00"), filepath.Join(root, "latest")); err != nil {
		t.Skip("symlinks unavailable here")
	}

	list, err := ListFolders(root)
	if err != nil {
		t.Fatal(err)
	}
	n, err := CountFolders(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(list) {
		t.Errorf("CountFolders = %d, ListFolders = %d — the picker and the menu would report different stores", n, len(list))
	}
	if n != 3 {
		t.Errorf("counted %d folders, want 3 (a loose file and a symlink are neither)", n)
	}

	// An unreadable root is an error in both, so a misconfigured path still
	// reads as "unreadable" in the picker rather than as an empty store.
	if _, err := CountFolders(filepath.Join(root, "does-not-exist")); err == nil {
		t.Error("a missing root reported no error; the picker would show it as empty rather than broken")
	}
}

// TestFolderCountsDoNotWalkTheTree — ListFolders stats every file under every
// subfolder to size and order a MENU. Three callers wanted only the number and
// paid that walk for it, two of them rendering on the agent editor: 273ms of a
// 296ms source listing on one deployment, spent computing sizes it discarded.
//
// Asserted at the call sites because the cost is invisible at a glance — both
// calls read as "get the folders, take the length", and only the body of the
// one being called says what that costs.
func TestFolderCountsDoNotWalkTheTree(t *testing.T) {
	// Precise rather than clever: a ListFolders whose result is rendered as a
	// NUMBER within the next couple of lines. source.go's Fetch also calls
	// ListFolders and legitimately needs the walk — it takes the NEWEST
	// subfolder, which is the modtime the walk computes — so a rule that
	// flagged every length check would flag that too and teach people to
	// ignore it.
	for _, f := range []string{"source.go", "admin.go", "filestore.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "ListFolders(") {
				continue
			}
			window := strings.Join(lines[i:min(i+3, len(lines))], "\n")
			if strings.Contains(window, "strconv.Itoa(len(") {
				t.Errorf("%s:%d renders a COUNT from ListFolders — use CountFolders, which is one directory read instead of a recursive walk of every subfolder", f, i+1)
			}
		}
	}
}
