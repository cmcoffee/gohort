package anvil

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// makeBundle is a repository of two commits, bundled. extra is more git
// bundle create arguments (a range, say); none means --all.
func makeBundle(t *testing.T, files map[string]string, extra ...string) string {
	t.Helper()
	origin := t.TempDir()
	initRepo(t, origin)
	mustWrite(t, filepath.Join(origin, "README"), "one\n")
	commitAll(t, origin, "c0")
	for name, body := range files {
		mustWrite(t, filepath.Join(origin, name), body)
	}
	mustWrite(t, filepath.Join(origin, "README"), "two\n")
	commitAll(t, origin, "c1")
	if len(extra) == 0 {
		extra = []string{"--all"}
	}
	bundle := filepath.Join(t.TempDir(), "proj.bundle")
	args := append([]string{"-C", origin, "bundle", "create", bundle}, extra...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return bundle
}

// A bundle's header names its branches and which one HEAD is; a bundle made
// from a range, or something that is not a bundle, cannot start a project.
func TestABundlesHeaderIsReadBeforeAnythingElse(t *testing.T) {
	h, err := readBundleHeader(makeBundle(t, nil))
	if err != nil || len(h.Branches) != 1 || h.Head != h.Branches[0] {
		t.Fatalf("header %+v %v", h, err)
	}
	if _, err := readBundleHeader(makeBundle(t, nil, "HEAD~1..HEAD")); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("a bundle with prerequisites: %v", err)
	}
	junk := filepath.Join(t.TempDir(), "x.bundle")
	mustWrite(t, junk, "PK\x03\x04 not a bundle")
	if _, err := readBundleHeader(junk); err == nil || !strings.Contains(err.Error(), "not a git bundle") {
		t.Errorf("not a bundle: %v", err)
	}
}

// bundleRoutes is an Anvil behind its real routes with a user and an
// administrator, a workspaces folder, and unpacks run inline.
func bundleRoutes(t *testing.T) (T *Anvil, serve func(user, method, path string, body []byte, ctype string) *httptest.ResponseRecorder) {
	t.Helper()
	auth := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return auth }
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "root", "pw", true)
	prevWS := WorkspacesDir()
	SetWorkspacesDir(t.TempDir())
	prevStart := startUnpack
	startUnpack = func(fn func()) { fn() }
	t.Cleanup(func() {
		AuthDB, startUnpack = prevAuth, prevStart
		SetWorkspacesDir(prevWS)
		bundleJobsMu.Lock()
		bundleJobs = map[string]*bundleJob{}
		bundleJobsMu.Unlock()
	})
	T = &Anvil{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.Routes()
	serve = func(user, method, path string, body []byte, ctype string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	return T, serve
}

func multipartBundle(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filepath.Base(path))
	fw.Write(data)
	mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}

// A user brings a repository as a bundle once an administrator allows it: it
// becomes a checkout of their own with the bundle's history, no remote, and
// no address, and its settings save without one.
func TestAUserBringsARepositoryAsABundle(t *testing.T) {
	T, serve := bundleRoutes(t)
	body, ctype := multipartBundle(t, makeBundle(t, map[string]string{"main.go": "package main\n"}))
	if w := serve("alice", "POST", "/api/bundle/upload", body, ctype); w.Code != http.StatusForbidden {
		t.Fatalf("a user's upload while users may not: %d %s", w.Code, w.Body.String())
	}
	T.DB.Set(bundleSettingsTable, bundleSettingsKey, bundleSettings{UsersMay: true})
	if w := serve("alice", "POST", "/api/bundle/upload", body, ctype); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	w := serve("alice", "POST", "/api/bundle/finalize", []byte(`{}`), "application/json")
	var fin struct{ ID string }
	if json.Unmarshal(w.Body.Bytes(), &fin); w.Code != 200 || fin.ID == "" {
		t.Fatalf("finalize: %d %s", w.Code, w.Body.String())
	}
	if w := serve("alice", "GET", "/api/bundle/status", nil, ""); !strings.Contains(w.Body.String(), `"state":"ready"`) {
		t.Fatalf("status: %s", w.Body.String())
	}
	udb := UserDB(T.DB, "alice")
	var p Project
	if !udb.Get(projectTable, fin.ID, &p) || p.Source != sourceBundle || p.BundleName != "proj.bundle" || p.Name != "proj" || p.Unpack != "" || p.RepoURL != "" {
		t.Fatalf("project %+v", p)
	}
	base, _ := p.baseDir()
	if log, err := runGit(base, "log", "--oneline"); err != nil || strings.Count(strings.TrimSpace(log), "\n") != 1 {
		t.Errorf("history: %q %v", log, err)
	}
	// Read from the checkout's own config: Anvil's git runs with settings
	// that name origin, so asking git would always list one.
	if cfg, _ := os.ReadFile(filepath.Join(base, ".git", "config")); strings.Contains(string(cfg), "[remote") {
		t.Errorf("the checkout kept a remote naming the upload:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(base, "main.go")); err != nil {
		t.Error("the bundle's files are not in the checkout")
	}
	if dir, _ := uploadsDir("alice"); dirSize(dir) != 0 {
		t.Error("the uploaded bundle was left behind")
	}

	// Its settings save without an address, and a save cannot change where
	// it came from.
	save := fmt.Sprintf(`{"id":%q,"name":"Renamed","kind":"clone","source":"","bundle_name":"x","repo_url":"https://example.test/r.git"}`, fin.ID)
	if w := serve("alice", "POST", "/api/projects", []byte(save), "application/json"); w.Code != 200 {
		t.Fatalf("saving its settings: %d %s", w.Code, w.Body.String())
	}
	udb.Get(projectTable, fin.ID, &p)
	if p.Name != "Renamed" || p.Source != sourceBundle || p.BundleName != "proj.bundle" || p.RepoURL != "" {
		t.Errorf("after a save: %+v", p)
	}
	// Nor can a new project claim to be one.
	if w := serve("root", "POST", "/api/projects", []byte(`{"name":"N","kind":"clone","source":"bundle"}`), "application/json"); w.Code == 200 {
		t.Errorf("a project without an address claimed to be a bundle: %s", w.Body.String())
	}
}

// The upload is refused past the size limit, and an unpack that grows past the
// person's disk is stopped and removed, saying why.
func TestABundleIsHeldToTheLimits(t *testing.T) {
	T, serve := bundleRoutes(t)
	T.DB.Set(bundleSettingsTable, bundleSettingsKey, bundleSettings{UsersMay: true, MaxUploadMB: 1, DiskMB: 2})
	noise := make([]byte, 3<<20)
	rand.Read(noise)
	big := makeBundle(t, map[string]string{"noise.bin": string(noise)})
	body, ctype := multipartBundle(t, big)
	if w := serve("alice", "POST", "/api/bundle/upload", body, ctype); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "1 MB") {
		t.Errorf("an oversize bundle: %d %s", w.Code, w.Body.String())
	}

	prevTick := bundleTick
	bundleTick = 20 * time.Millisecond
	t.Cleanup(func() { bundleTick = prevTick })
	T.DB.Set(bundleSettingsTable, bundleSettingsKey, bundleSettings{UsersMay: true, MaxUploadMB: 1, DiskMB: 1})
	udb := UserDB(T.DB, "alice")
	p := Project{ID: "b1", Name: "b", Kind: KindClone, Owner: "alice", Source: sourceBundle, Branch: "master", Unpack: unpackRunning}
	if h, err := readBundleHeader(big); err == nil {
		p.Branch = h.Head
	}
	T.saveProject(udb, p)
	base, _ := p.baseDir()
	staged := filepath.Join(t.TempDir(), "in.bundle")
	data, _ := os.ReadFile(big)
	os.WriteFile(staged, data, 0o600)
	T.unpackBundle(udb, p, base, staged)
	udb.Get(projectTable, "b1", &p)
	if p.Unpack != unpackFailed || !strings.Contains(p.UnpackError, "disk limit") {
		t.Errorf("an unpack past the disk: %+v", p)
	}
	if _, err := os.Stat(base); err == nil {
		t.Error("the overgrown checkout was left on disk")
	}
	if line, ok := bundleCardState(p); !ok || !strings.Contains(line, "disk limit") {
		t.Errorf("the card: %q", line)
	}
}

// The unpack reads only git's file transport, without the server's git
// config: a filter the host's config names does not run on the checkout.
func TestABundleUnpacksWithoutTheServersGitConfig(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	marker := filepath.Join(home, "ran")
	mustWrite(t, filepath.Join(home, ".gitconfig"), "[filter \"x\"]\n\tsmudge = sh -c 'touch "+marker+"; cat'\n\trequired = true\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	bundle := makeBundle(t, map[string]string{".gitattributes": "*.txt filter=x\n", "a.txt": "x\n"})
	dir := filepath.Join(t.TempDir(), "c")
	os.MkdirAll(dir, 0o700)
	h, _ := readBundleHeader(bundle)
	if out, err := cloneBundle(t.Context(), dir, bundle, h.Head); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a filter from the server's git config ran on the checkout")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Error("the file the filter would have smudged is missing")
	}
}

// The unpack's own environment turns off the global and system git config,
// for a sandbox backend that would otherwise show git the server's home.
func TestTheUnpackIgnoresHostGitConfig(t *testing.T) {
	env := bundleCloneEnv()
	if env["GIT_CONFIG_GLOBAL"] != "/dev/null" || env["GIT_CONFIG_NOSYSTEM"] != "1" || env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("env %v", env)
	}
}

// A project removed while it unpacks stays removed, whether the unpack then
// fails or succeeds.
func TestAProjectRemovedWhileAFailedUnpackStaysRemoved(t *testing.T) {
	T, _ := bundleRoutes(t)
	udb := UserDB(T.DB, "alice")
	junk := filepath.Join(t.TempDir(), "x.bundle")
	mustWrite(t, junk, "# v2 git bundle\n0000000000000000000000000000000000000000 refs/heads/main\n\nnot a pack")
	p := Project{ID: "gone2", Name: "g", Kind: KindClone, Owner: "alice", Source: sourceBundle, Branch: "main", Unpack: unpackRunning}
	base, _ := p.baseDir()
	T.unpackBundle(udb, p, base, junk)
	var cur Project
	if udb.Get(projectTable, "gone2", &cur) {
		t.Errorf("a failed unpack brought back a removed project: %+v", cur)
	}
	if j, _ := jobFor("alice"); j.State != "failed" {
		t.Errorf("the job: %+v", j)
	}
}

// A project removed while it unpacks stays removed.
func TestAProjectRemovedWhileItUnpacksStaysRemoved(t *testing.T) {
	T, _ := bundleRoutes(t)
	udb := UserDB(T.DB, "alice")
	bundle := makeBundle(t, nil)
	h, _ := readBundleHeader(bundle)
	p := Project{ID: "gone", Name: "g", Kind: KindClone, Owner: "alice", Source: sourceBundle, Branch: h.Head, Unpack: unpackRunning}
	base, _ := p.baseDir()
	T.unpackBundle(udb, p, base, bundle)
	var cur Project
	if udb.Get(projectTable, "gone", &cur) {
		t.Errorf("the unpack brought back a removed project: %+v", cur)
	}
	if _, err := os.Stat(base); err == nil {
		t.Error("the removed project's checkout was left on disk")
	}
}
