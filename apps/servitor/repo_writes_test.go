package servitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// countCommits counts the store commits made through repoCommit, failing the
// ones fail says to.
func countCommits(t *testing.T, fail func(n int) bool) *int {
	t.Helper()
	n := 0
	prev := repoCommit
	repoCommit = func(store Database, fn func(b DBBatch) error) error {
		n++
		if fail != nil && fail(n) {
			return errors.New("disk full")
		}
		return prev(store, fn)
	}
	t.Cleanup(func() { repoCommit = prev })
	return &n
}

// ingestFolder is a local repo of files, ingested by an admin, and what it
// takes to ingest it again.
func ingestFolder(t *testing.T, files map[string]string) (dir string, ingest func() Appliance) {
	t.Helper()
	withRepoUsers(t)
	prevStore := RepoFilesDB
	RepoFilesDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RepoFilesDB = prevStore })
	dir = filepath.Join(t.TempDir(), "app")
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644)
	}
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "root-admin")
	udb.Set(applianceTable, "repo-1", Appliance{ID: "repo-1", Name: "app", Type: "repo", RepoPath: dir, Owner: "root-admin"})
	return dir, func() Appliance {
		app.cloneAndIngestRepo(context.Background(), "root-admin", udb, "repo-1")
		var rec Appliance
		udb.Get(applianceTable, "repo-1", &rec)
		return rec
	}
}

func fiveFiles() map[string]string {
	files := map[string]string{}
	for i := 1; i <= 5; i++ {
		files[fmt.Sprintf("pkg/f%d.go", i)] = fmt.Sprintf("package pkg // file %d\n", i)
	}
	return files
}

// An ingest writes its files a group at a time, each file with its hash, and
// a refresh writes only what changed and drops what went, in a commit of its
// own rather than one per file.
func TestAnIngestWritesAGroupAtATime(t *testing.T) {
	prev := repoWriteFiles
	repoWriteFiles = 2
	t.Cleanup(func() { repoWriteFiles = prev })
	dir, ingest := ingestFolder(t, fiveFiles())
	commits := countCommits(t, nil)
	if rec := ingest(); rec.RepoFiles != 5 || rec.RepoCloned == "" {
		t.Fatalf("record after ingest: %d files, refreshed %q", rec.RepoFiles, rec.RepoCloned)
	}
	if *commits != 3 {
		t.Errorf("five files in groups of two: %d commits, want 3", *commits)
	}
	store := repoFileStore("root-admin", "repo-1")
	for i := 1; i <= 5; i++ {
		p := fmt.Sprintf("pkg/f%d.go", i)
		var h string
		if body, ok := readRepoFile("root-admin", "repo-1", p); !ok || !strings.Contains(body, fmt.Sprintf("file %d", i)) {
			t.Errorf("%s: %q", p, body)
		}
		if !store.Get(repoHashTable, p, &h) || h == "" {
			t.Errorf("%s was written without its hash", p)
		}
	}

	*commits = 0
	ingest()
	if *commits != 0 {
		t.Errorf("a refresh with nothing changed made %d commits", *commits)
	}

	os.Remove(filepath.Join(dir, "pkg/f1.go"))
	os.Remove(filepath.Join(dir, "pkg/f2.go"))
	os.WriteFile(filepath.Join(dir, "pkg/f3.go"), []byte("package pkg // changed\n"), 0o644)
	*commits = 0
	if rec := ingest(); rec.RepoFiles != 3 {
		t.Errorf("record after a refresh: %d files, want 3", rec.RepoFiles)
	}
	if *commits != 2 {
		t.Errorf("one changed file and two gone: %d commits, want 2 (the change, then the drops)", *commits)
	}
	for _, p := range []string{"pkg/f1.go", "pkg/f2.go"} {
		var h string
		if _, ok := readRepoFile("root-admin", "repo-1", p); ok || store.Get(repoHashTable, p, &h) {
			t.Errorf("%s is gone from the folder but not from the store", p)
		}
	}
	if body, _ := readRepoFile("root-admin", "repo-1", "pkg/f3.go"); !strings.Contains(body, "changed") {
		t.Errorf("a changed file was not rewritten: %q", body)
	}
}

// A group is bounded by its size as well as its count: it is held in memory
// until it is written.
func TestAGroupIsBoundedBySize(t *testing.T) {
	prev := repoWriteBytes
	repoWriteBytes = 30
	t.Cleanup(func() { repoWriteBytes = prev })
	_, ingest := ingestFolder(t, map[string]string{
		"a.go": strings.Repeat("a", 20) + "\n",
		"b.go": strings.Repeat("b", 20) + "\n",
		"c.go": strings.Repeat("c", 20) + "\n",
	})
	commits := countCommits(t, nil)
	ingest()
	if *commits != 2 {
		t.Errorf("three 21-byte files under a 30-byte bound: %d commits, want 2", *commits)
	}
}

// A store that refuses a write stops the ingest and leaves the record as it
// was: no file's hash is stored without its content, so the next ingest
// writes everything this one could not.
func TestAFailedWriteIsWrittenNextTime(t *testing.T) {
	prev := repoWriteFiles
	repoWriteFiles = 2
	t.Cleanup(func() { repoWriteFiles = prev })
	_, ingest := ingestFolder(t, fiveFiles())
	commits := countCommits(t, func(n int) bool { return n == 2 })
	rec := ingest()
	if rec.RepoFiles != 0 || rec.RepoCloned != "" {
		t.Errorf("an ingest that failed to write was recorded as done: %d files, refreshed %q", rec.RepoFiles, rec.RepoCloned)
	}
	if *commits != 2 {
		t.Errorf("the ingest went on writing after a failure: %d commits", *commits)
	}
	store := repoFileStore("root-admin", "repo-1")
	if got := len(store.Keys(repoHashTable)); got != 2 {
		t.Errorf("hashes stored after the first group and a failed second: %d, want 2", got)
	}
	*commits = 100 // fail nothing from here
	if rec = ingest(); rec.RepoFiles != 5 {
		t.Errorf("the next ingest: %d files, want 5", rec.RepoFiles)
	}
	for i := 1; i <= 5; i++ {
		if _, ok := readRepoFile("root-admin", "repo-1", fmt.Sprintf("pkg/f%d.go", i)); !ok {
			t.Errorf("pkg/f%d.go was not written on the next ingest", i)
		}
	}
}

// Wiping a repo's files is one commit, not one per file.
func TestAWipeIsOneCommit(t *testing.T) {
	_, ingest := ingestFolder(t, fiveFiles())
	ingest()
	commits := countCommits(t, nil)
	wipeRepoFiles("root-admin", "repo-1")
	if *commits != 1 {
		t.Errorf("wiping five files: %d commits, want 1", *commits)
	}
	store := repoFileStore("root-admin", "repo-1")
	if n := len(store.Keys(repoFilesTable)) + len(store.Keys(repoHashTable)); n != 0 {
		t.Errorf("%d rows left after a wipe", n)
	}
}
