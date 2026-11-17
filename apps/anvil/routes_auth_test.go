package anvil

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// anvilRoutes is an Anvil with a directory project bob may open and a
// checkout of alice's, behind the real routes, with alice, bob, carol and an
// administrator signed up. It answers a request's status, and a GET's body.
func anvilRoutes(t *testing.T) (do func(user, method, path, body string) int, get func(user, path string) string) {
	t.Helper()
	auth := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	for _, u := range []string{"alice", "bob", "carol"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthSetUser(auth, "root", "pw", true)
	T := &Anvil{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/README", []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	T.saveProject(nil, Project{ID: "root1", Name: "Tree", Kind: KindRoot, Root: dir, AllowedUsers: []string{"bob"}})
	T.saveProject(UserDB(T.DB, "alice"), Project{ID: "clone1", Name: "Mine", Kind: KindClone, RepoURL: "https://example.test/r.git"})
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.Routes()
	serve := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	do = func(user, method, path, body string) int { return serve(user, method, path, body).Code }
	get = func(user, path string) string { return serve(user, "GET", path, "").Body.String() }
	return do, get
}

// Every JSON route refuses a signed-out request, answers someone who may open
// the project, refuses a method it does not take, and answers someone who
// may not open it as if it were not there.
func TestEveryJSONRouteRefusesWhoItShould(t *testing.T) {
	do, get := anvilRoutes(t)
	for _, rt := range []struct{ method, path, body, wrong string }{
		{"GET", "/api/projects", "", "PUT"},
		{"POST", "/api/projects", `{"id":"clone1","name":"Mine, renamed","kind":"clone","repo_url":"https://example.test/r.git"}`, ""},
	} {
		if code := do("", rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("alice", rt.method, rt.path, rt.body); code >= 300 {
			t.Errorf("alice, %s %s answered %d", rt.method, rt.path, code)
		}
		if rt.wrong != "" {
			if code := do("alice", rt.wrong, rt.path, ""); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
			}
		}
	}
	// The directory project: bob may open it, carol may not.
	for _, rt := range []struct{ method, path, wrong string }{
		{"GET", "/api/project/root1", "PUT"},
		{"GET", "/api/project/root1/status", "PUT"},
		{"GET", "/api/project/root1/worktree", "PUT"},
		{"GET", "/api/edits?project_id=root1", "PUT"},
		{"POST", "/api/project/root1/deepen", "GET"},
	} {
		if code := do("", rt.method, rt.path, ""); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("bob", rt.method, rt.path, ""); code == http.StatusUnauthorized || code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			t.Errorf("bob, %s %s answered %d", rt.method, rt.path, code)
		}
		if code := do("carol", rt.method, rt.path, ""); code != http.StatusNotFound {
			t.Errorf("carol, %s %s answered %d, want 404", rt.method, rt.path, code)
		}
		if code := do("bob", rt.wrong, rt.path, ""); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
		}
	}
	// A checkout is its owner's alone.
	for _, who := range []string{"bob", "carol", "root"} {
		if code := do(who, "GET", "/api/project/clone1", ""); code != http.StatusNotFound {
			t.Errorf("%s opened alice's checkout: %d", who, code)
		}
		if code := do(who, "DELETE", "/api/project/clone1", ""); code != http.StatusNotFound {
			t.Errorf("%s deleting alice's checkout answered %d, want 404", who, code)
		}
	}
	// Registering a directory on this host is an administrator's.
	if code := do("alice", "POST", "/api/projects", `{"name":"Root try","kind":"root","root":"/tmp"}`); code != http.StatusForbidden {
		t.Errorf("alice registering a directory answered %d, want 403", code)
	}
	// Running a project's command is an administrator's.
	action := "/api/project/root1/action/nope"
	if code := do("", "POST", action, ""); code != http.StatusUnauthorized {
		t.Errorf("signed out, a command answered %d, want 401", code)
	}
	if code := do("carol", "POST", action, ""); code != http.StatusNotFound {
		t.Errorf("carol, a command answered %d, want 404", code)
	}
	if code := do("bob", "POST", action, ""); code != http.StatusForbidden {
		t.Errorf("bob running a command on the directory project answered %d, want 403", code)
	}
	if code := do("root", "GET", action, ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET of a command answered %d, want 405", code)
	}
	if code := do("alice", "GET", "/api/project/clone1", ""); code != http.StatusOK {
		t.Errorf("alice opening her own checkout answered %d", code)
	}
	// Removing a directory project is an administrator's too: being able to
	// open one is not being able to take it away from everyone else.
	if code := do("bob", "DELETE", "/api/project/root1", ""); code != http.StatusForbidden {
		t.Errorf("bob removing the directory project answered %d, want 403", code)
	}
	if code := do("bob", "GET", "/api/project/root1", ""); code != http.StatusOK {
		t.Errorf("after a refused removal bob's project answered %d", code)
	}
	if body := get("bob", "/api/projects"); !strings.Contains(body, `"can_remove":false`) || strings.Contains(body, `"can_remove":true`) {
		t.Errorf("bob's list offers Remove on the directory project: %s", body)
	}
	if body := get("root", "/api/projects"); !strings.Contains(body, `"can_remove":true`) {
		t.Errorf("the administrator's list does not offer Remove: %s", body)
	}
	if code := do("root", "DELETE", "/api/project/root1", ""); code != http.StatusOK {
		t.Errorf("the administrator removing it answered %d", code)
	}
}

// Who works on what: adding a project (a repository from elsewhere, or a
// directory on this host) is an administrator's. A user keeps the checkout
// they have and may change how Anvil works on it, but not point it at another
// repository or branch.
func TestOnlyAnAdministratorAddsAProject(t *testing.T) {
	do, get := anvilRoutes(t)
	if code := do("alice", "POST", "/api/projects", `{"name":"New","repo_url":"https://example.test/n.git"}`); code != http.StatusForbidden {
		t.Errorf("a user added a repository: %d", code)
	}
	if code := do("root", "POST", "/api/projects", `{"name":"New","repo_url":"https://example.test/n.git"}`); code >= 300 {
		t.Errorf("an administrator could not add a repository: %d", code)
	}
	if code := do("alice", "POST", "/api/projects", `{"id":"clone1","name":"Mine","kind":"clone","repo_url":"https://example.test/r.git","instructions":"Use make.","check_command":"make test"}`); code >= 300 {
		t.Errorf("a user could not change how Anvil works on their checkout: %d", code)
	}
	if body := get("alice", "/api/project/clone1"); !strings.Contains(body, "make test") {
		t.Errorf("the change was not saved: %s", body)
	}
	// Run commands without asking is a checkout's; a directory keeps asking.
	if code := do("alice", "POST", "/api/projects", `{"id":"clone1","name":"Mine","kind":"clone","repo_url":"https://example.test/r.git","run_without_asking":true}`); code >= 300 {
		t.Errorf("a user could not let their checkout run commands without asking: %d", code)
	}
	if body := get("alice", "/api/project/clone1"); !strings.Contains(body, `"run_without_asking":true`) {
		t.Errorf("run without asking was not saved: %s", body)
	}
	rootDir := t.TempDir()
	if code := do("root", "POST", "/api/projects", `{"id":"root1","name":"Host","kind":"root","root":"`+rootDir+`","run_without_asking":true}`); code >= 300 {
		t.Errorf("saving the directory project failed: %d", code)
	}
	if body := get("root", "/api/project/root1"); strings.Contains(body, `"run_without_asking":true`) {
		t.Errorf("a directory project kept run without asking: %s", body)
	}
	for _, body := range []string{
		`{"id":"clone1","name":"Mine","kind":"clone","repo_url":"https://example.test/other.git"}`,
		`{"id":"clone1","name":"Mine","kind":"clone","repo_url":"https://example.test/r.git","branch":"dev"}`,
	} {
		if code := do("alice", "POST", "/api/projects", body); code != http.StatusForbidden {
			t.Errorf("a user repointed their checkout (%s): %d", body, code)
		}
	}
	// New project is everyone's; adding one by its address is in an
	// administrator's menu only.
	if page := get("alice", "/"); strings.Contains(page, "Add a repository or a directory") || !strings.Contains(page, "New project") || !strings.Contains(page, "adding one by its address is an administrator") || !strings.Contains(page, "Start a new repository") {
		t.Error("a user's Anvil home offers to add a repository by its address, does not say who does, or does not offer a new repository")
	}
	if page := get("root", "/"); !strings.Contains(page, "Add a repository or a directory") || !strings.Contains(page, "Start a new repository") {
		t.Error("an administrator's New project menu lacks adding a repository or starting one")
	}
	if code := do("alice", "GET", "/projects", ""); code != http.StatusFound {
		t.Errorf("the old projects page answered %d, want a redirect home", code)
	}
}
