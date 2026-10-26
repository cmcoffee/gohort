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

// Every JSON route registerHandlers builds on jsonapi, read from its
// registration: a signed-out request is refused and never served, and a
// method the route does not take is 405. The settings and active-run routes
// are built by their own functions (exposed.go serves them too), so they are
// named here instead.
//
// Only refusals are asked for, of a method or a caller that never reaches a
// handler, so nothing here starts a session, drafts with a model or answers
// an approval.
func TestEveryJSONRouteRefusesWhoItShould(t *testing.T) {
	src, err := os.ReadFile("orchestrate.go")
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

	type route struct{ path, expr string }
	var routes []route
	reg := regexp.MustCompile(`T\.HandleFunc\("(/api/[^"]+)", ((?:[^\n]|\n\t\t)*)`)
	for _, m := range reg.FindAllStringSubmatch(string(src), -1) {
		if strings.Contains(m[2], "jsonapi.") {
			routes = append(routes, route{m[1], m[2]})
		}
	}
	if len(routes) < 30 {
		t.Fatalf("only %d jsonapi routes found; the pattern has stopped matching", len(routes))
	}
	routes = append(routes,
		route{"/api/runs/active", "jsonapi.Get("},
		route{"/api/settings/private", "jsonapi.Get("},
		route{"/api/settings/memory", "jsonapi.Get("},
		route{"/api/settings/private/set", `jsonapi.Methods{"POST"`},
		route{"/api/settings/memory/set", `jsonapi.Methods{"POST"`},
	)
	for _, rt := range routes {
		getOK := strings.HasPrefix(rt.expr, "jsonapi.Get(") || strings.Contains(rt.expr, `"GET":`)
		probe := http.MethodPost
		if getOK {
			probe = http.MethodGet
		}
		if code := do("", probe, rt.path); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("signed out, %s %s answered %d, want a refusal", probe, rt.path, code)
		}
		if !strings.Contains(rt.expr, `"PUT"`) {
			if code := do("alice", http.MethodPut, rt.path); code != http.StatusMethodNotAllowed {
				t.Errorf("PUT %s answered %d, want 405", rt.path, code)
			}
		}
		if !getOK {
			if code := do("alice", http.MethodGet, rt.path); code != http.StatusMethodNotAllowed {
				t.Errorf("GET %s answered %d, want 405", rt.path, code)
			}
		}
		// A read takes no write: these were GET-only before the port.
		if strings.HasPrefix(rt.expr, "jsonapi.Get(") {
			if code := do("alice", http.MethodDelete, rt.path); code != http.StatusMethodNotAllowed {
				t.Errorf("DELETE %s answered %d, want 405", rt.path, code)
			}
		}
	}
}
