package anvil

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// startNew starts a repository as user and returns it with its checkout.
func startNew(t *testing.T, T *Anvil, serve func(user, method, path string, body []byte, ctype string) *httptest.ResponseRecorder, user, name string) (Project, string) {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	w := serve(user, "POST", "/api/projects/new", []byte(fmt.Sprintf(`{"name":%q}`, name)), "application/json")
	var out struct{ ID string }
	if json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || out.ID == "" {
		t.Fatalf("starting %s: %d %s", name, w.Code, w.Body.String())
	}
	var p Project
	if !UserDB(T.DB, user).Get(projectTable, out.ID, &p) {
		t.Fatal("the project was not saved")
	}
	base, err := p.baseDir()
	if err != nil {
		t.Fatal(err)
	}
	return p, base
}

func postJSON(serve func(user, method, path string, body []byte, ctype string) *httptest.ResponseRecorder, user, path, body string) *httptest.ResponseRecorder {
	return serve(user, "POST", path, []byte(body), "application/json")
}

// bareRepo is an empty repository to push to.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return dir
}

func remoteLog(t *testing.T, dir, branch string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--format=%an %s", branch).CombinedOutput()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Anyone may start a repository: it is theirs, on main, with one empty
// commit so Changes and the branch line work from the first edit.
func TestAnyoneStartsANewRepository(t *testing.T) {
	T, serve := bundleRoutes(t)
	p, base := startNew(t, T, serve, "alice", "Fresh")
	if p.Source != sourceNew || p.Kind != KindClone || p.Owner != "alice" || p.Name != "Fresh" || p.RepoURL != "" {
		t.Fatalf("project %+v", p)
	}
	info := inspectRepo(base)
	if !info.IsRepo || info.Branch != "main" || info.Dirty {
		t.Fatalf("checkout %+v", info)
	}
	if log, _ := runGit(base, "log", "--format=%an %s"); log != "alice Started in Anvil" {
		t.Errorf("history: %q", log)
	}
	if w := postJSON(serve, "alice", "/api/projects/new", `{"name":"  "}`); w.Code != 400 {
		t.Errorf("no name: %d", w.Code)
	}
	// Held to the person's Anvil disk, as a bundle is.
	T.DB.Set(bundleSettingsTable, bundleSettingsKey, bundleSettings{MaxUploadMB: 1, DiskMB: 1})
	mustWrite(t, filepath.Join(base, "big"), strings.Repeat("x", 2<<20))
	if w := postJSON(serve, "alice", "/api/projects/new", `{"name":"Another"}`); w.Code != 409 {
		t.Errorf("past the disk limit: %d %s", w.Code, w.Body.String())
	}
}

// Commit records everything that differs, new files included, as the person
// pressing it, and only with a message.
func TestCommitRecordsTheWorkAsThePerson(t *testing.T) {
	T, serve := bundleRoutes(t)
	p, base := startNew(t, T, serve, "alice", "Fresh")
	api := "/api/project/" + p.ID + "/commit"
	if w := serve("alice", "GET", api, nil, ""); !strings.Contains(w.Body.String(), "Nothing differs") {
		t.Errorf("clean: %s", w.Body.String())
	}
	mustWrite(t, filepath.Join(base, "main.go"), "package main\n")
	if w := serve("alice", "GET", api, nil, ""); !strings.Contains(w.Body.String(), "1 file: main.go") {
		t.Errorf("one new file: %s", w.Body.String())
	}
	if w := postJSON(serve, "alice", api, `{"message":" "}`); w.Code != 400 {
		t.Errorf("no message: %d", w.Code)
	}
	if w := postJSON(serve, "alice", api, `{"message":"Add main"}`); w.Code != 200 || !strings.Contains(w.Body.String(), "Committed 1 file.") {
		t.Fatalf("commit: %d %s", w.Code, w.Body.String())
	}
	if log, _ := runGit(base, "log", "-1", "--format=%an <%ae> %s"); log != "alice <alice@users.gohort.invalid> Add main" {
		t.Errorf("the commit: %q", log)
	}
	if inspectRepo(base).Dirty {
		t.Error("the checkout is still dirty")
	}
	if w := postJSON(serve, "alice", api, `{"message":"Again"}`); w.Code != 409 {
		t.Errorf("nothing to commit: %d", w.Code)
	}
}

// Anvil commits only in a checkout it made, and never an app's, whose commits
// are its installs.
func TestCommitIsOnlyForACheckoutAnvilMade(t *testing.T) {
	cases := []struct {
		p    Project
		user string
		ok   bool
	}{
		{Project{Kind: KindClone, Owner: "alice", Source: sourceNew}, "alice", true},
		{Project{Kind: KindClone, Owner: "alice", Source: sourceBundle}, "alice", true},
		{Project{Kind: KindClone, Owner: "alice"}, "alice", true},
		{Project{Kind: KindClone, Owner: "alice", Source: sourceApp}, "alice", false},
		{Project{Kind: KindClone, Owner: "alice", Source: sourceBundle, Unpack: unpackRunning}, "alice", false},
		{Project{Kind: KindRoot, Root: "/srv/x"}, "root", false},
	}
	for i, c := range cases {
		if got := commitRefusal(c.p, c.user, c.user == "root") == ""; got != c.ok {
			t.Errorf("case %d %+v: may commit %v", i, c.p, got)
		}
	}
}

// Publish commits what is pending when given a message, pushes the history
// to a branch elsewhere, keeps where it went for next time, and never reads
// the checkout's own git config on the server.
func TestAPublishPushesTheCommittedHistory(t *testing.T) {
	T, serve := bundleRoutes(t)
	p, base := startNew(t, T, serve, "root", "Fresh")
	remote := bareRepo(t)
	// Config the run tool could have written. Read by a push run in the
	// checkout, it would send the history somewhere else: the server's own
	// git settings pin hooks and helpers, not where a url is rewritten to.
	elsewhere := bareRepo(t)
	if _, err := runGit(base, "config", "url."+elsewhere+".pushInsteadOf", remote); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(base, "a.go"), "package a\n")

	api := "/api/project/" + p.ID + "/publish"
	if w := serve("root", "GET", api, nil, ""); !strings.Contains(w.Body.String(), `"push_branch":"main"`) || !strings.Contains(w.Body.String(), "Not sent unless you commit them") {
		t.Errorf("the form: %s", w.Body.String())
	}
	w := postJSON(serve, "root", api, fmt.Sprintf(`{"push_url":%q,"message":"Add a"}`, remote))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Committed 1 file. Pushed main to") {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	if got := remoteLog(t, remote, "main"); len(got) != 2 || got[0] != "root Add a" {
		t.Errorf("the remote's history: %q", got)
	}
	if got := remoteLog(t, elsewhere, "main"); got != nil {
		t.Errorf("the checkout's config sent the push elsewhere: %q", got)
	}
	var saved Project
	UserDB(T.DB, "root").Get(projectTable, p.ID, &saved)
	if saved.PushURL != remote || saved.PushBranch != "main" || saved.PushedAt.IsZero() {
		t.Errorf("where it went: %+v", saved)
	}

	// What is not committed is not sent, and the reply says so.
	mustWrite(t, filepath.Join(base, "b.go"), "package a\n")
	w = postJSON(serve, "root", api, fmt.Sprintf(`{"push_url":%q,"push_branch":"work"}`, remote))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "1 uncommitted file(s) were not sent") {
		t.Errorf("an uncommitted file: %d %s", w.Code, w.Body.String())
	}
	if got := remoteLog(t, remote, "work"); len(got) != 2 {
		t.Errorf("the second branch: %q", got)
	}

	// A remote with history this project lacks is not overwritten.
	other := bareRepo(t)
	seed := t.TempDir()
	initRepo(t, seed)
	mustWrite(t, filepath.Join(seed, "x"), "x\n")
	commitAll(t, seed, "theirs")
	if out, err := exec.Command("git", "-C", seed, "push", "-q", other, "HEAD:refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	w = postJSON(serve, "root", api, fmt.Sprintf(`{"push_url":%q}`, other))
	if w.Code != 502 || !strings.Contains(w.Body.String(), "has commits this project does not") {
		t.Errorf("a diverged remote: %d %s", w.Code, w.Body.String())
	}
	if got := remoteLog(t, other, "main"); len(got) != 1 {
		t.Errorf("the diverged remote changed: %q", got)
	}
}

// A user publishes only their own repository, only to an https address on
// the public internet, and not at all once an administrator turns it off.
func TestAUserPublishesOnlyTheirOwnToAPublicAddress(t *testing.T) {
	T, serve := bundleRoutes(t)
	p, _ := startNew(t, T, serve, "alice", "Fresh")
	api := "/api/project/" + p.ID + "/publish"
	if w := postJSON(serve, "alice", api, `{"push_url":"/srv/repos/x.git"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "https") {
		t.Errorf("a local path: %d %s", w.Code, w.Body.String())
	}
	prev := lookupHost
	t.Cleanup(func() { lookupHost = prev })
	lookupHost = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
	}
	if w := postJSON(serve, "alice", api, `{"push_url":"https://git.internal.test/x.git"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "private or internal") {
		t.Errorf("an internal host: %d %s", w.Code, w.Body.String())
	}
	if why := T.publishRefusal(p, "bob", false); !strings.Contains(why, "not yours") {
		t.Errorf("someone else's: %q", why)
	}

	// Turned off: no Publish for a user, though the bundle stays.
	if w := postJSON(serve, "root", "/api/publish/settings", `{"users_may_publish":false}`); w.Code != 200 {
		t.Fatalf("settings: %d %s", w.Code, w.Body.String())
	}
	if w := serve("alice", "GET", api, nil, ""); w.Code != 403 || !strings.Contains(w.Body.String(), "turned off") {
		t.Errorf("publish while off: %d %s", w.Code, w.Body.String())
	}
	var names []string
	for _, a := range T.toolbarActions(p, "alice", false) {
		names = append(names, a.Label)
	}
	if got := strings.Join(names, ","); strings.Contains(got, "Publish") || !strings.Contains(got, "The repository as a bundle") || !strings.Contains(got, "Commit") {
		t.Errorf("the toolbar while off: %s", got)
	}
	if w := serve("alice", "POST", "/api/publish/settings", []byte(`{"users_may_publish":true}`), "application/json"); w.Code != 403 {
		t.Errorf("a user changed the setting: %d", w.Code)
	}
	if w := serve("root", "GET", "/api/publish/settings", nil, ""); !strings.Contains(w.Body.String(), `"users_may_publish":false`) {
		t.Errorf("the setting reads back: %s", w.Body.String())
	}
}

// A saved token goes only to the host it was saved for, never back to the
// browser, and a settings save cannot change where the project publishes.
func TestThePushTokenStaysWithItsHost(t *testing.T) {
	if !sameHost("https://git.example.com/a.git", "https://GIT.example.com/b.git") {
		t.Error("the same host, another repository")
	}
	for _, other := range []string{"https://evil.example.net/a.git", "http://git.example.com/a.git", "", "https://git.example.com:8443/a.git"} {
		if sameHost("https://git.example.com/a.git", other) {
			t.Errorf("%q counted as the same host", other)
		}
	}

	T, serve := bundleRoutes(t)
	p, _ := startNew(t, T, serve, "alice", "Fresh")
	udb := UserDB(T.DB, "alice")
	udb.Get(projectTable, p.ID, &p)
	p.PushURL, p.PushBranch, p.PushToken, p.PushedAt = "https://git.example.com/a.git", "main", "s3cret-token", time.Now()
	T.saveProject(udb, p)

	if w := serve("alice", "GET", "/api/project/"+p.ID, nil, ""); strings.Contains(w.Body.String(), "s3cret") || !strings.Contains(w.Body.String(), `"has_push_token":true`) {
		t.Errorf("the project as the browser gets it: %s", w.Body.String())
	}
	if w := serve("alice", "GET", "/api/project/"+p.ID+"/publish", nil, ""); strings.Contains(w.Body.String(), "s3cret") {
		t.Errorf("the publish form: %s", w.Body.String())
	}
	save := fmt.Sprintf(`{"id":%q,"name":"Renamed","kind":"clone","push_url":"https://evil.example.net/x.git","push_token":"","pushed_at":"2001-01-01T00:00:00Z"}`, p.ID)
	if w := serve("alice", "POST", "/api/projects", []byte(save), "application/json"); w.Code != 200 {
		t.Fatalf("saving its settings: %d %s", w.Code, w.Body.String())
	}
	var got Project
	udb.Get(projectTable, p.ID, &got)
	if got.Name != "Renamed" || got.PushURL != p.PushURL || got.PushToken != "s3cret-token" || !got.PushedAt.Equal(p.PushedAt) {
		t.Errorf("after a settings save: %+v", got)
	}
}

// A push refused for a reason says the reason, not git's line naming the
// remote.
func TestAPushFailureSaysWhy(t *testing.T) {
	cases := map[string]string{
		"To https://h/r.git\n ! [rejected]        main -> main (fetch first)\nerror: failed to push": "has commits this project does not",
		"remote: Invalid username or password.\nfatal: Authentication failed for 'https://h/r.git/'": "check the token",
		"remote: Repository not found.\nfatal: repository 'https://h/r.git/' not found":              "no repository answered",
		"To https://h/r.git\nerror: something else broke":                                            "error: something else broke",
	}
	for text, want := range cases {
		if got := pushFailure(text, fmt.Errorf("git push failed")); !strings.Contains(got, want) {
			t.Errorf("%q: %q", text, got)
		}
	}
}

// Download as a bundle is the committed history, which git clones at home;
// the reply counts what was left out. A directory on this host does not
// download whole.
func TestDownloadAsABundleCarriesTheHistory(t *testing.T) {
	T, serve := bundleRoutes(t)
	p, base := startNew(t, T, serve, "alice", "Fresh")
	mustWrite(t, filepath.Join(base, "a.go"), "package a\n")
	postJSON(serve, "alice", "/api/project/"+p.ID+"/commit", `{"message":"Add a"}`)
	mustWrite(t, filepath.Join(base, "b.go"), "package a\n")

	w := serve("alice", "GET", "/api/project/"+p.ID+"/bundle", nil, "")
	if w.Code != 200 || w.Header().Get("X-Anvil-Uncommitted") != "1" || !strings.HasSuffix(strings.TrimSuffix(w.Header().Get("Content-Disposition"), `"`), ".bundle") {
		t.Fatalf("download: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	file := filepath.Join(t.TempDir(), "x.bundle")
	os.WriteFile(file, w.Body.Bytes(), 0o600)
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", "-q", file, clone).CombinedOutput(); err != nil {
		t.Fatalf("cloning the bundle: %v %s", err, out)
	}
	if got := remoteLog(t, clone, "main"); len(got) != 2 || got[0] != "alice Add a" {
		t.Errorf("the bundle's history: %q", got)
	}

	root := Project{ID: "r1", Name: "Dir", Kind: KindRoot, Root: base}
	T.saveProject(nil, root)
	if w := serve("root", "GET", "/api/project/r1/bundle", nil, ""); w.Code != 409 {
		t.Errorf("a directory on this host: %d %s", w.Code, w.Body.String())
	}
}
