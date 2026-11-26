package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// relocationIn is a dbRelocation over dir that records what it logs; set
// names the [paths] store settings that are set.
func relocationIn(dir string, notes *[]string, set ...string) dbRelocation {
	return dbRelocation{
		dataDir: dir,
		set: func(s string) bool {
			for _, x := range set {
				if x == s {
					return true
				}
			}
			return false
		},
		link: os.Symlink,
		note: func(v ...interface{}) { *notes = append(*notes, fmt.Sprintf(v[0].(string), v[1:]...)) },
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// A fresh data folder opens everything in db/ and moves nothing.
func TestAFreshDataFolderPutsTheDatabasesInDB(t *testing.T) {
	dir := t.TempDir()
	var notes []string
	l, err := relocationIn(dir, &notes).run()
	if err != nil {
		t.Fatal(err)
	}
	if l.mainFile() != filepath.Join(dir, "db", "gohort.db") || l.appFile("customapp_x_1") != filepath.Join(dir, "db", "apps", "gohort_customapp_x_1.db") {
		t.Fatalf("layout %+v", l)
	}
	if fi, err := os.Stat(l.apps); err != nil || !fi.IsDir() {
		t.Fatalf("db/apps was not made: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("a fresh folder logged %q", notes)
	}
}

// The old layout's files move into db/ with a link left at each old path; a
// store whose setting is set stays put, and a second start changes nothing.
func TestTheOldLayoutMovesIntoDBOnceWithALinkAtEachOldPath(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"gohort.db", "gohort_vectors.db", "gohort_repos.db", "gohort_bundles.db", "gohort_customapp_notes_abc.db"} {
		writeFile(t, filepath.Join(dir, n), "body of "+n)
	}
	var notes []string
	l, err := relocationIn(dir, &notes, "repo_dir").run()
	if err != nil {
		t.Fatal(err)
	}
	if l.dir != filepath.Join(dir, "db") {
		t.Fatalf("layout %+v", l)
	}
	moved := map[string]string{
		"gohort.db":                     filepath.Join(dir, "db", "gohort.db"),
		"gohort_vectors.db":             filepath.Join(dir, "db", "gohort_vectors.db"),
		"gohort_bundles.db":             filepath.Join(dir, "db", "gohort_bundles.db"),
		"gohort_customapp_notes_abc.db": filepath.Join(dir, "db", "apps", "gohort_customapp_notes_abc.db"),
	}
	for n, to := range moved {
		if readFile(t, to) != "body of "+n {
			t.Errorf("%s did not arrive at %s", n, to)
		}
		old := filepath.Join(dir, n)
		if !isLink(old) || readFile(t, old) != "body of "+n {
			t.Errorf("%s: no working link at the old path", n)
		}
		if target, _ := os.Readlink(old); filepath.IsAbs(target) {
			t.Errorf("%s: link %q is absolute, so a moved data folder breaks it", n, target)
		}
	}
	if isLink(filepath.Join(dir, "gohort_repos.db")) || readFile(t, filepath.Join(dir, "gohort_repos.db")) != "body of gohort_repos.db" {
		t.Error("a store whose setting is set was moved")
	}
	if len(notes) != len(moved) || !strings.Contains(notes[0], "a link stays at the old path") {
		t.Fatalf("notes %q", notes)
	}
	if _, err := os.Lstat(filepath.Join(dir, "db", ".link-check")); err == nil {
		t.Error("the link check was left behind")
	}

	notes = nil
	if _, err := relocationIn(dir, &notes, "repo_dir").run(); err != nil || len(notes) != 0 {
		t.Fatalf("a second start: %v, %q", err, notes)
	}
}

// An older binary opening the old path reaches the moved database through
// the link, with what it holds.
func TestAnOlderBinaryOpensTheMovedDatabaseThroughTheLink(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "gohort.db")
	st, err := kvlite.Open(old)
	if err != nil {
		t.Fatal(err)
	}
	st.Set("users", "alice", "kept")
	st.Close()
	var notes []string
	if _, err := relocationIn(dir, &notes).run(); err != nil {
		t.Fatal(err)
	}
	st, err = kvlite.Open(old)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var v string
	if found, err := st.Get("users", "alice", &v); !found || err != nil || v != "kept" {
		t.Fatalf("through the link read %q", v)
	}
}

// Where no link can be made, nothing moves and the old layout is opened.
func TestWithoutLinksTheOldLayoutIsKept(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gohort.db"), "main")
	writeFile(t, filepath.Join(dir, "gohort_customapp_a_1.db"), "app")
	var notes []string
	r := relocationIn(dir, &notes)
	r.link = func(string, string) error { return errors.New("a required privilege is not held by the client") }
	l, err := r.run()
	if err != nil {
		t.Fatal(err)
	}
	if l.mainFile() != filepath.Join(dir, "gohort.db") || l.appFile("customapp_a_1") != filepath.Join(dir, "gohort_customapp_a_1.db") {
		t.Fatalf("layout %+v", l)
	}
	if readFile(t, filepath.Join(dir, "gohort.db")) != "main" || isLink(filepath.Join(dir, "gohort.db")) {
		t.Fatal("the main database moved with no link to leave")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "no link can be made") {
		t.Fatalf("notes %q", notes)
	}
	// A fresh folder there needs no link and uses db/.
	fresh := relocationIn(t.TempDir(), &notes)
	fresh.link = r.link
	if l, err := fresh.run(); err != nil || filepath.Base(l.dir) != "db" {
		t.Fatalf("fresh without links: %+v, %v", l, err)
	}
}

// A regular file at both paths: the new one is opened, neither is touched,
// and the log names both.
func TestBothCopiesPresentOpensTheNewOneAndWarns(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "db"), 0o755)
	writeFile(t, filepath.Join(dir, "gohort.db"), "old")
	writeFile(t, filepath.Join(dir, "db", "gohort.db"), "new")
	var notes []string
	l, err := relocationIn(dir, &notes).run()
	if err != nil {
		t.Fatal(err)
	}
	if l.mainFile() != filepath.Join(dir, "db", "gohort.db") || readFile(t, l.mainFile()) != "new" || readFile(t, filepath.Join(dir, "gohort.db")) != "old" {
		t.Fatal("a copy was touched, or the old one opened")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], filepath.Join(dir, "gohort.db")) || !strings.Contains(notes[0], filepath.Join(dir, "db", "gohort.db")) {
		t.Fatalf("notes %q", notes)
	}
}

// A rename that fails stops the start, and the file stays where it was.
func TestAFailedMoveRefusesToStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root renames into a read-only folder")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gohort.db"), "main")
	db := filepath.Join(dir, "db")
	os.MkdirAll(filepath.Join(db, "apps"), 0o755)
	r := relocationIn(dir, new([]string))
	// The link check passes; the folder is read-only by the time of the move.
	r.link = func(target, name string) error {
		if strings.HasSuffix(name, ".link-check") {
			return os.Chmod(db, 0o555)
		}
		return os.Symlink(target, name)
	}
	t.Cleanup(func() { os.Chmod(db, 0o755) })
	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "does not copy a database") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, filepath.Join(dir, "gohort.db")) != "main" {
		t.Fatal("the database left its old path")
	}
}

// A link that cannot be left after a move puts the file back and stops the
// start, so an older binary is never left without its database.
func TestALinkThatFailsAfterAMoveMovesTheFileBack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gohort.db"), "main")
	r := relocationIn(dir, new([]string))
	r.link = func(target, name string) error {
		if strings.HasSuffix(name, ".link-check") {
			return os.Symlink(target, name)
		}
		return errors.New("link refused")
	}
	_, err := r.run()
	if err == nil || !strings.Contains(err.Error(), "it was moved back") {
		t.Fatalf("err = %v", err)
	}
	if isLink(filepath.Join(dir, "gohort.db")) || readFile(t, filepath.Join(dir, "gohort.db")) != "main" {
		t.Fatal("the database is not back at its old path")
	}
	if _, err := os.Lstat(filepath.Join(dir, "db", "gohort.db")); err == nil {
		t.Fatal("a copy stayed in db/")
	}
}

// Removing an app's file takes the link at its old path with it; a file never
// made is no error.
func TestRemovingAnAppFileTakesItsOldLink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gohort_customapp_a_1.db"), "app")
	l, err := relocationIn(dir, new([]string)).run()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.removeAppFile(dir, "customapp_a_1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{l.appFile("customapp_a_1"), filepath.Join(dir, "gohort_customapp_a_1.db")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there", p)
		}
	}
	if err := l.removeAppFile(dir, "customapp_never_2"); err != nil {
		t.Fatalf("a file never made: %v", err)
	}
	// In the old layout the file is the old path, removed once.
	legacy := dbLayout{dir: dir, apps: dir}
	writeFile(t, filepath.Join(dir, "gohort_customapp_b_1.db"), "b")
	if err := legacy.removeAppFile(dir, "customapp_b_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "gohort_customapp_b_1.db")); !os.IsNotExist(err) {
		t.Error("the old layout's file stayed")
	}
}
