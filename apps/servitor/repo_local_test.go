package servitor

// A repo can be a folder on the gohort server, read where it is. It reads the
// server's own disk, so only an admin may name one, it may not overlap
// gohort's own folders, and it is held to the same file rules as a clone.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withRepoUsers(t *testing.T) {
	t.Helper()
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "root-admin", "pw", true)
	AuthSetUser(auth, "alice", "pw", false)
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
}

func TestALocalRepoFolderIsAnAdminsAndStaysOutOfGohortsOwn(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "project")
	data := filepath.Join(tmp, "data")
	for _, d := range []string{project, filepath.Join(data, "workspaces"), filepath.Join(data, "notes")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(project, "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o644)
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(data, "workspaces"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })

	if got, err := repoLocalRoot(project, true); err != nil || got == "" {
		t.Fatalf("an admin's project folder was refused: %v", err)
	}
	for _, c := range []struct {
		path  string
		admin bool
		why   string
	}{
		{project, false, "a non-admin owner"},
		{"relative/dir", true, "a relative path"},
		{filepath.Join(tmp, "missing"), true, "a folder that is not there"},
		{file, true, "a file"},
		{"/", true, "the filesystem root"},
		{filepath.Join(data, "notes"), true, "a folder inside gohort's data folder"},
		{tmp, true, "a folder holding gohort's data folder"},
		{project + "\x01x", true, "a path with a control character"},
	} {
		if _, err := repoLocalRoot(c.path, c.admin); err == nil {
			t.Errorf("%s (%q) was accepted", c.why, c.path)
		}
	}
	// A symlink is resolved before the checks, so it cannot carry a folder
	// inside gohort's data past them.
	link := filepath.Join(tmp, "innocent")
	if err := os.Symlink(filepath.Join(data, "notes"), link); err == nil {
		if _, err := repoLocalRoot(link, true); err == nil {
			t.Error("a symlink into gohort's data folder was accepted")
		}
	}
}

// The folder is ingested where it is, under a clone's rules: text files only,
// no symlink followed out of it, skipped directories left out, and a file
// gone from the folder gone from the store on the next refresh.
func TestALocalFolderIsIngestedWhereItIs(t *testing.T) {
	withRepoUsers(t)
	prevStore := RepoFilesDB
	RepoFilesDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RepoFilesDB = prevStore })

	tmp := t.TempDir()
	dir := filepath.Join(tmp, "app")
	outside := filepath.Join(tmp, "outside-secret.txt")
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "dep", "index.js"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0, 1, 2, 0}, 0o644)
	os.WriteFile(outside, []byte("not part of the folder"), 0o644)
	os.Symlink(outside, filepath.Join(dir, "linked.txt"))

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "root-admin")
	udb.Set(applianceTable, "repo-1", Appliance{ID: "repo-1", Name: "app", Type: "repo", RepoPath: dir, Owner: "root-admin"})
	app.cloneAndIngestRepo(context.Background(), "root-admin", udb, "repo-1")

	for _, p := range []string{"main.go", "pkg/util.go"} {
		if _, ok := readRepoFile("root-admin", "repo-1", p); !ok {
			t.Errorf("%s was not ingested", p)
		}
	}
	for _, p := range []string{"linked.txt", "blob.bin", "node_modules/dep/index.js"} {
		if _, ok := readRepoFile("root-admin", "repo-1", p); ok {
			t.Errorf("%s should not have been ingested", p)
		}
	}
	var rec Appliance
	udb.Get(applianceTable, "repo-1", &rec)
	if rec.RepoFiles != 2 || rec.RepoCloned == "" {
		t.Errorf("record after ingest: %d files, refreshed %q", rec.RepoFiles, rec.RepoCloned)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Error("the folder itself must be left alone")
	}

	os.Remove(filepath.Join(dir, "pkg", "util.go"))
	app.cloneAndIngestRepo(context.Background(), "root-admin", udb, "repo-1")
	if _, ok := readRepoFile("root-admin", "repo-1", "pkg/util.go"); ok {
		t.Error("a file removed from the folder stayed in the store")
	}

	// The same record owned by a non-admin is not read at all.
	audb := UserDB(app.DB, "alice")
	audb.Set(applianceTable, "repo-2", Appliance{ID: "repo-2", Name: "app", Type: "repo", RepoPath: dir, Owner: "alice"})
	app.cloneAndIngestRepo(context.Background(), "alice", audb, "repo-2")
	if repoFileCount("alice", "repo-2") != 0 {
		t.Error("a non-admin's local folder was ingested")
	}
}

// Saving: only an admin adds a local folder, a bad folder is refused before
// the record exists, and a folder carries no git remote or token.
func TestSavingALocalRepo(t *testing.T) {
	withRepoUsers(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	dir := t.TempDir()
	post := func(user, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", user, body))
		return w
	}
	if w := post("alice", `{"type":"repo","repo_path":"`+dir+`"}`); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin adding a local folder got %d: %s", w.Code, w.Body.String())
	}
	if w := post("root-admin", `{"type":"repo","repo_path":"relative/dir"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a relative folder got %d: %s", w.Code, w.Body.String())
	}
	w := post("root-admin", `{"type":"repo","repo_path":"`+dir+`","repo_url":"https://host-a/org/repo","repo_token":"tok-1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("an admin's folder got %d: %s", w.Code, w.Body.String())
	}
	var saved Appliance
	for _, k := range UserDB(app.DB, "root-admin").Keys(applianceTable) {
		UserDB(app.DB, "root-admin").Get(applianceTable, k, &saved)
	}
	if saved.RepoPath != dir || saved.RepoURL != "" || saved.RepoToken != "" || saved.Name != filepath.Base(dir) {
		t.Errorf("saved: path %q url %q token %q name %q", saved.RepoPath, saved.RepoURL, saved.RepoToken, saved.Name)
	}
	if !strings.Contains(localRepoAdminOnly, "only an admin") {
		t.Error("the refusal should say who may")
	}
}
