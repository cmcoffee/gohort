package servitor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A repo can be a folder on the gohort server, read where it is. It reads the
// server's own disk, so only an admin may name one, it may not overlap
// gohort's own folders, and it is held to the same file rules as a clone.

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
	// The save starts a clone in the background; it must be done before the
	// next test swaps the repo store it reads.
	t.Cleanup(repoIngests.Wait)
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

// A repo appliance is cloned by git on the gohort server itself, from a URL
// any user typed. Only plain remote sources may reach that command line.

func TestRepoSourceRefusesWhatGitWouldRunOrReadLocally(t *testing.T) {
	for _, c := range []struct{ url, branch string }{
		{"--upload-pack=touch /tmp/x", ""},
		{"-oProxyCommand=x", ""},
		{"ext::sh -c touch% /tmp/x", ""},
		{"file:///etc", ""},
		{"/var/lib/gohort/repo", ""},
		{"../other", ""},
		{"git://host-a/repo.git", ""},
		{"http://host-a/repo.git", ""},
		{"fd::3", ""},
		{"https://host-a/repo.git\n--upload-pack=x", ""},
		{"https://-oProxyCommand=x/repo", ""},
		{"ssh://-oProxyCommand=x/repo", ""},
		{"https://host-a/repo.git", "--upload-pack=x"},
		{"https://host-a/repo.git", "main..HEAD"},
		{"https://host-a/repo.git", "a b"},
		{"", ""},
	} {
		if err := validateRepoSource(c.url, c.branch, true); err == nil {
			t.Errorf("url %q branch %q was accepted", c.url, c.branch)
		}
	}
}

func TestRepoSourceAcceptsPlainRemotes(t *testing.T) {
	for _, u := range []string{
		"https://host-a/org/repo.git",
		"https://host-a:8443/org/repo",
		"ssh://git@host-a/org/repo.git",
		"git@host-a:org/repo.git",
	} {
		if err := validateRepoSource(u, "release/1.2", true); err != nil {
			t.Errorf("%q refused for an admin owner: %v", u, err)
		}
	}
	// ssh authenticates with the server's own keys: an admin's call.
	for _, u := range []string{"ssh://git@host-a/org/repo.git", "git@host-a:org/repo.git"} {
		if err := validateRepoSource(u, "", false); err == nil {
			t.Errorf("%q accepted for a non-admin owner; it would clone with the server's ssh keys", u)
		}
	}
	if err := validateRepoSource("https://host-a/org/repo.git", "", false); err != nil {
		t.Errorf("https refused for a non-admin owner: %v", err)
	}
}

// withRepoFiles puts files straight into one repo's encrypted store, the
// state an ingest leaves, without a clone.
func withRepoFiles(t *testing.T, user, id string, files map[string]string) {
	t.Helper()
	prev := RepoFilesDB
	RepoFilesDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RepoFilesDB = prev })
	store := repoFileStore(user, id)
	for path, body := range files {
		store.Set(repoFilesTable, path, body)
	}
}

// search_code shares the log search's matcher, so a query means the same in
// code as in a log: case-insensitive, literal by default so a bracket finds
// itself, a regular expression on request, with exclude and a path filter,
// and a cut and an exclusion that are both said.
func TestRepoSearchSharesTheLogSearchMatcher(t *testing.T) {
	withRepoFiles(t, "alice", "repo-1", map[string]string{
		"auth/token.go":      "package auth\n// GetUser( is the old name\nfunc GetUser(id int) {}\nfunc SetUser(u User) {}\n",
		"auth/token_test.go": "func TestGetUser(t *testing.T) { GetUser(1) }\n",
		"web/handlers.js":    "getUser(42)\n",
	})
	ctx := context.Background()
	find := func(q repoQuery) []string {
		t.Helper()
		res, err := searchRepo(ctx, "alice", "repo-1", q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		var out []string
		for _, h := range res.Hits {
			out = append(out, fmt.Sprintf("%s:%d", h.Path, h.Line))
		}
		return out
	}
	// Literal, case-insensitive: the bracket is just a bracket.
	if got := fmt.Sprint(find(repoQuery{Text: "getuser("})); got != "[auth/token.go:2 auth/token.go:3 auth/token_test.go:1 web/handlers.js:1]" {
		t.Errorf("literal search = %s", got)
	}
	if got := fmt.Sprint(find(repoQuery{Text: `func (get|set)user\(`, Regex: true})); got != "[auth/token.go:3 auth/token.go:4]" {
		t.Errorf("regex search = %s", got)
	}
	if got := fmt.Sprint(find(repoQuery{Text: "getuser(", Exclude: `^\s*//`, Glob: "*.go"})); got != "[auth/token.go:3 auth/token_test.go:1]" {
		t.Errorf("exclude + glob = %s", got)
	}
	if got := fmt.Sprint(find(repoQuery{Text: "getuser", Glob: "handlers"})); got != "[web/handlers.js:1]" {
		t.Errorf("a path fragment as glob = %s", got)
	}

	res, _ := searchRepo(ctx, "alice", "repo-1", repoQuery{Text: "user", MaxHits: 2})
	if !res.Truncated || !strings.Contains(res.notes(), "first 2 matches; there may be more") {
		t.Errorf("a cut search must say so: truncated=%v notes=%q", res.Truncated, res.notes())
	}
	q := repoQuery{Text: "getuser", Exclude: "getuser"}
	res, _ = searchRepo(ctx, "alice", "repo-1", q)
	if len(res.Hits) != 0 || !strings.HasPrefix(res.noMatches(q), "No matches left after exclude: 4 line(s)") {
		t.Errorf("an exclude that hid everything read as %q", res.noMatches(q))
	}
	q = repoQuery{Text: "getuser", Glob: "*.py"}
	if res, _ := searchRepo(ctx, "alice", "repo-1", q); !strings.Contains(res.noMatches(q), "The rest of the repository was not searched") {
		t.Errorf("a glob-narrowed miss read as %q", res.noMatches(q))
	}

	if _, err := searchRepo(ctx, "alice", "repo-1", repoQuery{Text: "(", Regex: true}); err == nil || !strings.Contains(err.Error(), "query") {
		t.Errorf("a bad regex should name the query, got %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := searchRepo(cancelled, "alice", "repo-1", repoQuery{Text: "user"}); err == nil {
		t.Error("a cancelled search ran to the end")
	}
}

// The tool end to end: the new options arrive from arguments, and the reply
// carries the cut.
func TestSearchCodeToolTakesTheSharedOptions(t *testing.T) {
	withRepoFiles(t, "alice", "repo-1", map[string]string{
		"a.go": "func A() {}\nfunc B() {}\n",
		"b.md": "func A is documented here\n",
	})
	var search AgentToolDef
	for _, td := range repoCodeTools("alice", "repo-1") {
		if td.Tool.Name == "search_code" {
			search = td
		}
	}
	for _, p := range []string{"query", "regex", "exclude", "glob"} {
		if _, ok := search.Tool.Parameters[p]; !ok {
			t.Errorf("search_code has no %q parameter", p)
		}
	}
	out, err := search.Handler(context.Background(), map[string]any{"query": "func [ab]", "regex": true, "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "a.go:1: func A() {}\na.go:2: func B() {}\n" {
		t.Errorf("reply = %q", out)
	}
}

// search_code reads several files at once and merges in path order, so the
// answer must be a single pass's: the same hits in the same order, the same
// cut, and exclusions counted only up to where the limit stopped the search.
func TestRepoSearchInParallelGivesTheSerialAnswer(t *testing.T) {
	files := map[string]string{}
	for f := 0; f < 20; f++ {
		var b strings.Builder
		for i := 0; i < 30+f*7; i++ {
			fmt.Fprintf(&b, []string{"func Handle%d() {}\n", "// TODO handle %d\n", "x := handle(%d)\n", "return nil // %d\n"}[(i+f)%4], i)
		}
		files[fmt.Sprintf("pkg%02d/file.go", f)] = b.String()
	}
	withRepoFiles(t, "alice", "repo-1", files)
	prev := bundle.SearchWorkers
	defer func() { bundle.SearchWorkers = prev }()
	run := func(workers int, q repoQuery) repoSearchResult {
		bundle.SearchWorkers = workers
		res, err := searchRepo(context.Background(), "alice", "repo-1", q)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for name, q := range map[string]repoQuery{
		"all":     {Text: "handle", MaxHits: 10000},
		"cut":     {Text: "handle", MaxHits: 75},
		"exclude": {Text: "handle", Exclude: "^//", MaxHits: 75},
		"regex":   {Text: `func Handle\d+`, Regex: true, MaxHits: 40},
		"glob":    {Text: "handle", Glob: "pkg1*", MaxHits: 30},
	} {
		serial, parallel := run(1, q), run(4, q)
		if !reflect.DeepEqual(serial, parallel) {
			t.Errorf("%s: serial and parallel differ: %d/%d hits, excluded %d/%d, truncated %v/%v", name,
				len(serial.Hits), len(parallel.Hits), serial.Excluded, parallel.Excluded, serial.Truncated, parallel.Truncated)
		}
	}

	// The cut, counted exactly: a file whose limit falls between two
	// excluded lines counts only the exclusion before the last kept hit.
	withRepoFiles(t, "alice", "repo-2", map[string]string{
		"a.go": "keep 1\n// keep x\nkeep 2\n// keep y\nkeep 3\n",
	})
	res, _ := searchRepo(context.Background(), "alice", "repo-2", repoQuery{Text: "keep", Exclude: "^//", MaxHits: 2})
	if len(res.Hits) != 2 || res.Excluded != 1 || !res.Truncated {
		t.Errorf("hits %d excluded %d truncated %v; want 2, 1, true", len(res.Hits), res.Excluded, res.Truncated)
	}
}
