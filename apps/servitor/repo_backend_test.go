package servitor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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

	q := repoQuery{Text: "user", MaxHits: 2}
	res, _ := searchRepo(ctx, "alice", "repo-1", q)
	if out := renderRepoSearch(res, q); !res.Truncated || !strings.HasPrefix(out, "Showing 2 of 3 matches counted in 1 of 3 files") {
		t.Errorf("a cut search must say so: truncated=%v reply=%q", res.Truncated, out)
	}
	q = repoQuery{Text: "getuser", Exclude: "getuser"}
	res, _ = searchRepo(ctx, "alice", "repo-1", q)
	if out := renderRepoSearch(res, q); len(res.Hits) != 0 || !strings.Contains(out, "But 4 line(s) matched the pattern and every one also matched exclude") {
		t.Errorf("an exclude that hid everything read as %q", out)
	}
	q = repoQuery{Text: "getuser", Glob: "*.py"}
	if res, _ := searchRepo(ctx, "alice", "repo-1", q); !strings.Contains(renderRepoSearch(res, q), "The rest of the repository was not searched") {
		t.Errorf("a glob-narrowed miss read as %q", renderRepoSearch(res, q))
	}
	q = repoQuery{Text: "no such name"}
	if res, _ := searchRepo(ctx, "alice", "repo-1", q); !strings.Contains(renderRepoSearch(res, q), "The text is not in the ingested source") {
		t.Errorf("a miss over the whole repo read as %q", renderRepoSearch(res, q))
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
	for _, p := range []string{"query", "patterns", "regex", "exclude", "glob", "context", "count", "group", "per_file"} {
		if _, ok := search.Tool.Parameters[p]; !ok {
			t.Errorf("search_code has no %q parameter", p)
		}
	}
	out, err := search.Handler(context.Background(), map[string]any{"query": "func [ab]", "regex": true, "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "2 match(es) in 1 file(s)") || !strings.HasSuffix(out, "\na.go:1: func A() {}\na.go:2: func B() {}\n") {
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
	run := func(workers int, q repoQuery) bundle.SearchResult {
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

	// The cut, counted whole: a file the limit falls inside is still read to
	// its end, so the reply can say how many there were and how many exclude
	// took out of the whole file.
	withRepoFiles(t, "alice", "repo-2", map[string]string{
		"a.go": "keep 1\n// keep x\nkeep 2\n// keep y\nkeep 3\n",
	})
	res, _ := searchRepo(context.Background(), "alice", "repo-2", repoQuery{Text: "keep", Exclude: "^//", MaxHits: 2})
	if len(res.Hits) != 2 || res.Total != 3 || res.Excluded != 2 || !res.Truncated {
		t.Errorf("hits %d of %d, excluded %d, truncated %v; want 2 of 3, 2, true", len(res.Hits), res.Total, res.Excluded, res.Truncated)
	}
}

// search_code runs on the log search (core/bundle) through repoSource, which
// keeps each file whole as one slice. It must find exactly what a plain scan
// of every line finds: every match, in path order, numbered right past the
// log search's slice height, and none lost where a file is passed over
// unsplit because no pattern could match it.
func TestRepoSearchFindsWhatAPlainScanFinds(t *testing.T) {
	files := map[string]string{}
	var big strings.Builder
	for i := 1; i <= bundle.SliceLines*2+500; i++ {
		if i%377 == 0 {
			fmt.Fprintf(&big, "\treturn retryTimeout(%d) // Timeout here\n", i)
		} else {
			fmt.Fprintf(&big, "\tx%d := compute(%d)\n", i, i)
		}
	}
	files["pkg/big.go"] = big.String()
	files["pkg/a.go"] = "func A() { retryTimeout(1) }\n// TIMEOUT in a comment\n"
	files["web/b.js"] = "nothing to see\nhere\n"
	files["web/c.js"] = "const timeout = 5\r\nretry(timeout)\r\n"
	withRepoFiles(t, "alice", "repo-1", files)

	plain := func(expr, exclude string) []string {
		re := regexp.MustCompile("(?i)" + expr)
		var ex *regexp.Regexp
		if exclude != "" {
			ex = regexp.MustCompile("(?i)" + exclude)
		}
		var paths []string
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var out []string
		for _, p := range paths {
			for i, line := range strings.Split(files[p], "\n") {
				if re.MatchString(line) && (ex == nil || !ex.MatchString(line)) {
					out = append(out, fmt.Sprintf("%s:%d", p, i+1))
				}
			}
		}
		return out
	}
	for _, c := range []struct {
		q       repoQuery
		re, exc string
	}{
		{repoQuery{Text: "timeout"}, "timeout", ""},
		{repoQuery{Text: "retryTimeout("}, `retrytimeout\(`, ""},
		{repoQuery{Text: `timeout\(\d+\)`, Regex: true}, `timeout\(\d+\)`, ""},
		{repoQuery{Text: "timeout", Exclude: "^\\s*//"}, "timeout", "^\\s*//"},
	} {
		c.q.MaxHits = 100000
		res, err := searchRepo(context.Background(), "alice", "repo-1", c.q)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range res.Hits {
			got = append(got, fmt.Sprintf("%s:%d", h.Path, h.Line))
		}
		if want := plain(c.re, c.exc); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v:\n got %v\nwant %v", c.q, got, want)
		}
	}
}

// The log search's options reach code: several texts in one pass, each in
// its own section; counts; groups; context lines; and a long reply paged.
func TestSearchCodeOffersTheLogSearchOptions(t *testing.T) {
	files := map[string]string{
		"auth/token.go": "func GetUser(id int) {}\nfunc SetUser(u User) {}\n// users_table holds them\n",
		"db/schema.sql": "CREATE TABLE users_table (id int);\n",
		"web/api.js":    "fetch('/api/v1/users')\nGetUser(7)\nGetUser(8)\n",
	}
	withRepoFiles(t, "alice", "repo-1", files)
	var search AgentToolDef
	for _, td := range repoCodeTools("alice", "repo-1") {
		if td.Tool.Name == "search_code" {
			search = td
		}
	}
	run := func(args map[string]any) string {
		t.Helper()
		out, err := search.Handler(context.Background(), args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	out := run(map[string]any{"patterns": []any{"GetUser(", "users_table", "/api/v2"}})
	for _, want := range []string{
		"3 patterns searched in one pass",
		`"GetUser(": 3 match(es) in 2 file(s)`,
		`"/api/v2": no matches`,
		"db/schema.sql:1: CREATE TABLE users_table (id int);",
		"web/api.js:3: GetUser(8)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("patterns reply is missing %q:\n%s", want, out)
		}
	}
	if out := run(map[string]any{"query": "getuser", "count": true}); !strings.Contains(out, "2  web/api.js") || !strings.Contains(out, "1  auth/token.go") {
		t.Errorf("count reply:\n%s", out)
	}
	if out := run(map[string]any{"query": "getuser(", "group": true}); !strings.Contains(out, "GetUser(<*>)") {
		t.Errorf("group reply should mask the arguments:\n%s", out)
	}
	if out := run(map[string]any{"query": "SetUser", "context": 1}); !strings.Contains(out, "1: func GetUser(id int) {}") || !strings.Contains(out, "3: // users_table holds them") {
		t.Errorf("context reply:\n%s", out)
	}
	var many strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&many, "call(%d) // GetUser path number %d\n", i, i)
	}
	files["gen/many.go"] = many.String()
	withRepoFiles(t, "alice", "repo-1", files)
	if out := run(map[string]any{"query": "getuser", "per_file": 2000, "context": 8}); !strings.Contains(out, `output_id="`) {
		t.Errorf("a long reply should arrive paged with a read_output trailer:\n%.300s", out[len(out)-300:])
	}
}

// GUARD: a repository's .git folder never reaches anything a tool can read.
// A clone's .git/config holds the URL it was cloned from, and with a token
// set that URL carries the token (repoCloneURL), so .git in the store would
// put a credential in reach of search_code and read_file. The repo ingest
// skips it, whatever the appliance's own skip list says, and submodules'
// too. Checked through the tools themselves, so a change to how repos are
// stored (through a bundle's ingest, say, which skips nothing) fails here.
func TestRepoIngestKeepsGitInternalsOutOfReach(t *testing.T) {
	t.Cleanup(repoIngests.Wait)
	withRepoUsers(t)
	withRepoFiles(t, "root-admin", "repo-g", nil)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	dir := t.TempDir()
	const secret = "tok-FAKE-0123456789"
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/config", "[remote \"origin\"]\n\turl = https://oauth2:"+secret+"@host-a/org/repo.git\n")
	write(".git/logs/HEAD", "clone: from https://oauth2:"+secret+"@host-a/org/repo.git\n")
	write("lib/sub/.git/config", "url = https://"+secret+"@host-a/org/sub.git\n")
	write("main.go", "package main // the code itself\n")
	udb := UserDB(app.DB, "root-admin")
	udb.Set(applianceTable, "repo-g", Appliance{ID: "repo-g", Type: "repo", Name: "repo-g", RepoPath: dir, RepoSkipDirs: []string{"docs"}})
	app.cloneAndIngestRepo(context.Background(), "root-admin", udb, "repo-g")

	if repoFileCount("root-admin", "repo-g") == 0 {
		t.Fatal("nothing was ingested: the guard below would prove nothing")
	}
	tools := map[string]AgentToolDef{}
	for _, td := range repoCodeTools("root-admin", "repo-g") {
		tools[td.Tool.Name] = td
	}
	ctx := context.Background()
	gitHit := regexp.MustCompile(`(?m)\.git/\S*:\d+:`)
	for _, args := range []map[string]any{
		{"query": secret},
		{"query": "oauth2"},
		{"query": "url =", "glob": ".git"},
	} {
		out, err := tools["search_code"].Handler(ctx, args)
		// The reply names the query and glob it was given, so the secret or
		// ".git" may be in it; a hit is a line naming a .git path.
		if err != nil || gitHit.MatchString(out) || !strings.Contains(out, "No matches") {
			t.Errorf("search_code %v reached .git: %v\n%s", args, err, out)
		}
	}
	for _, p := range []string{".git/config", ".git/logs/HEAD", "lib/sub/.git/config"} {
		if out, err := tools["read_file"].Handler(ctx, map[string]any{"path": p}); err == nil || strings.Contains(out, secret) {
			t.Errorf("read_file %s returned %q", p, out)
		}
	}
	for _, d := range []string{"", "lib/sub"} {
		out, _ := tools["list_dir"].Handler(ctx, map[string]any{"path": d})
		if strings.Contains(out, ".git") {
			t.Errorf("list_dir %q shows .git:\n%s", d, out)
		}
	}
}

// The scout searches a member's terms together, a batch of patterns at a
// time, and must keep exactly what one search per term kept: each term's
// first few hits in term order, each line once, up to the cap. Eleven terms,
// so two batches; some share lines, some match nothing, and the cap falls
// partway through.
func TestScoutKeepsWhatASearchPerTermKept(t *testing.T) {
	files := map[string]string{}
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo"}
	for f := 0; f < 6; f++ {
		var b strings.Builder
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&b, "line %d %s %s\n", i, words[(i+f)%len(words)], words[(i*3+f)%len(words)])
		}
		files[fmt.Sprintf("pkg%d/f.go", f)] = b.String()
	}
	withRepoFiles(t, "alice", "repo-1", files)
	ctx := context.Background()
	for _, terms := range [][]string{words, {"alpha"}, {"nothing", "matches", "here"}, append([]string{"zulu"}, words[:9]...)} {
		var want []repoSearchHit
		seen := map[string]bool{}
		for _, term := range terms {
			res, _ := searchRepo(ctx, "alice", "repo-1", repoQuery{Text: term, MaxHits: scoutPerTerm})
			for _, h := range res.Hits {
				if key := fmt.Sprintf("%s:%d", h.Path, h.Line); !seen[key] {
					seen[key] = true
					want = append(want, repoSearchHit{Path: h.Path, Line: h.Line, Text: h.Text})
				}
			}
			if len(want) >= scoutMaxHits {
				break
			}
		}
		searches := 0
		got := scoutHits(terms, func(batch []string) (bundle.SearchResult, error) {
			searches++
			return searchRepo(ctx, "alice", "repo-1", repoQuery{Text: batch[0], Patterns: batch[1:], MaxHits: scoutPerTerm})
		})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("terms %v:\n got %v\nwant %v", terms, got, want)
		}
		if limit := (len(terms) + bundle.MaxPatterns - 1) / bundle.MaxPatterns; searches > limit {
			t.Errorf("terms %v: %d searches, want at most %d", terms, searches, limit)
		}
	}
}

// search_code keeps the log search's limit, not a page's worth: the reply
// arrives as its first page, and read_output reaches a hit far past it
// without searching again.
func TestSearchCodeKeepsMoreThanAPage(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 700; i++ {
		fmt.Fprintf(&b, "handler%d := register(\"/route/%d\")\n", i, i)
	}
	withRepoFiles(t, "alice", "repo-1", map[string]string{"routes.go": b.String()})
	var search AgentToolDef
	for _, td := range repoCodeTools("alice", "repo-1") {
		if td.Tool.Name == "search_code" {
			search = td
		}
	}
	out, err := search.Handler(context.Background(), map[string]any{"query": "register("})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "700 match(es) in 1 file(s)") || strings.Contains(out, "/route/650\"") {
		t.Fatalf("the first page should open with the count and stop well short of hit 650:\n%.300s", out)
	}
	m := regexp.MustCompile(`output_id="([^"]+)"`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no read_output trailer:\n%s", out[len(out)-300:])
	}
	found, err := OutputPage{ID: m[1], Grep: "/route/650\"", Tool: "read_output"}.Read()
	if err != nil || !strings.Contains(found, "routes.go:650:") {
		t.Errorf("read_output should reach hit 650: %v\n%s", err, found)
	}
}
