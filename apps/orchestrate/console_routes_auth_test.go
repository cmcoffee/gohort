package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every console route, read from its registration: a signed-out request is
// refused (401, or 403 behind an administrator's gate) and never served, a
// method the route does not take is 405, and the administrator's routes
// refuse a signed-in user who is not one.
//
// Only refusals are asked for, and of a method or a caller that never reaches
// a handler, so nothing here runs an agent, a monitor or an approval.
func TestEveryConsoleRouteRefusesWhoItShould(t *testing.T) {
	src, err := os.ReadFile("console.go")
	if err != nil {
		t.Fatal(err)
	}
	prevRoot, prevAuth := RootDB, AuthDB
	root := &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	RootDB = root
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(auth, "alice", "pw", false)
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(user, method, path string) int {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}

	reg := regexp.MustCompile(`T\.HandleFunc\("(/api/[^"]+)"(\+verb)?, ((?:[^\n]|\n\t\t)*)`)
	n := 0
	for _, m := range reg.FindAllStringSubmatch(string(src), -1) {
		path, expr := m[1], m[3]
		if m[2] != "" {
			path += "link"
		}
		if strings.Contains(expr, "handleAgentImageRaw") || strings.Contains(expr, "handleConsoleFiringExport") {
			continue // a picture and a download: not JSON routes
		}
		n++
		getOK := strings.Contains(expr, "jsonapi.Get(")
		admin := strings.Contains(expr, "T.adminOpt()")
		probe, wrong := http.MethodGet, http.MethodPut
		switch {
		case strings.HasPrefix(expr, "jsonapi.Delete("):
			probe, wrong = http.MethodDelete, http.MethodPost
		case !getOK:
			probe, wrong = http.MethodPost, http.MethodGet
		}
		switch code := do("", probe, path); {
		case admin && code != http.StatusForbidden:
			t.Errorf("signed out, %s %s answered %d, want 403", probe, path, code)
		case !admin && code != http.StatusUnauthorized && code != http.StatusForbidden:
			t.Errorf("signed out, %s %s answered %d, want a refusal", probe, path, code)
		}
		if code := do("alice", wrong, path); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want 405", wrong, path, code)
		}
		// A route takes only the methods its registration names: no PUT
		// unless it says PUT.
		if !strings.Contains(expr, `"PUT"`) && !strings.HasPrefix(expr, "jsonapi.Put(") {
			if code := do("alice", http.MethodPut, path); code != http.StatusMethodNotAllowed {
				t.Errorf("PUT %s answered %d, want 405", path, code)
			}
		}
		// "admin only" routes refuse a user who is not one; asked by GET, so
		// nothing that acts is reached even if the gate went.
		if strings.Contains(expr, "T.adminOnlyOpt()") && getOK {
			if code := do("alice", http.MethodGet, path); code != http.StatusForbidden {
				t.Errorf("alice, not an administrator, GET %s answered %d, want 403", path, code)
			}
		}
		if admin {
			if code := do("alice", probe, path); code != http.StatusForbidden {
				t.Errorf("alice, not an administrator, %s %s answered %d, want 403", probe, path, code)
			}
		}
	}
	// The routes whose handler guarded its own methods inside the old
	// any-state-changing wrapper: they take what they took then and nothing
	// else. Named here rather than read from the registration, so a route
	// that loses its method list is caught.
	for _, path := range []string{"/api/console/agents/update", "/api/console/monitors/update", "/api/console/recurring/pause",
		"/api/console/recurring/update", "/api/console/permissions/promote", "/api/console/links/link", "/api/console/broken-tools/forget",
		"/api/reply-flag", "/api/console/credential-key/replace"} {
		if code := do("alice", http.MethodPut, path); code != http.StatusMethodNotAllowed {
			t.Errorf("PUT %s answered %d, want 405", path, code)
		}
	}
	// The Scheduler's Delete buttons send DELETE (page_chat.go), so these
	// take it: signed out, a DELETE reaches the sign-in refusal, not a 405.
	for _, path := range []string{"/api/console/agents/delete", "/api/console/monitors/delete", "/api/console/recurring/delete"} {
		if code := do("", http.MethodDelete, path); code != http.StatusUnauthorized {
			t.Errorf("signed out, DELETE %s answered %d, want 401", path, code)
		}
	}
	if n < 85 {
		t.Fatalf("only %d console routes found; the pattern has stopped matching", n)
	}
}
