package orchestrate

// The Agents workbench gate had never fired.
//
// It read AuthIsAdmin(T.DB, r), and an app's T.DB is
// global.db.Bucket("orchestrate") — a namespaced substore with no auth
// table. AuthHasUsers(T.DB) was false for the same reason, which
// short-circuited the condition before AuthIsAdmin was consulted. So
// agent CRUD, prompt editing, tool allowlists and memory pruning were
// reachable by any authenticated user, on a surface documented and
// intended as admin-only.
//
// Turning it on does not take agents away from anyone: the exposed-agent
// surface is apps/agents, on its own routes, and never comes through
// here.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func gateFixture(t *testing.T) (*OrchestrateApp, Database, func()) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return root }
	app := &OrchestrateApp{}
	// The substore an app really gets — the thing the old check read.
	app.DB = root.Bucket("orchestrate")
	return app, root, func() { AuthDB = prev }
}

func sessionReq(t *testing.T, root Database, user string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/orchestrate/api/agents", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(root, user)})
	return r
}

func TestAgentsWorkbenchIsAdminOnly(t *testing.T) {
	app, root, done := gateFixture(t)
	defer done()
	root.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true})
	root.Set(AuthTable, "user:temp", AuthUser{Username: "temp"})

	reached := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.adminGated(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})(w, r)
		return w
	}

	if w := reached(sessionReq(t, root, "temp")); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin reached the workbench: %d", w.Code)
	}
	if w := reached(sessionReq(t, root, "boss")); w.Code != http.StatusOK {
		t.Errorf("an admin was refused: %d %s", w.Code, w.Body.String())
	}
	// The refusal has to point somewhere, or a user who legitimately has
	// agents concludes they lost them.
	w := reached(sessionReq(t, root, "temp"))
	if body := w.Body.String(); !strings.Contains(body, "/agents/") {
		t.Errorf("the refusal should point at the surface they DO have: %s", body)
	}

	// The landing-page card follows the same policy, so the card and the
	// URL cannot disagree.
	if !app.WebRestricted(sessionReq(t, root, "temp")) {
		t.Error("the dashboard card should be hidden from a non-admin")
	}
	if app.WebRestricted(sessionReq(t, root, "boss")) {
		t.Error("the dashboard card should show for an admin")
	}
}

// With no users configured there is no admin to be. This is the property
// that makes enabling a previously-inert gate safe rather than a
// lockout.
func TestAgentsWorkbenchOpenWithoutAuth(t *testing.T) {
	app, _, done := gateFixture(t)
	defer done()

	w := httptest.NewRecorder()
	app.adminGated(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(w, httptest.NewRequest(http.MethodGet, "/orchestrate/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("a no-auth deployment was locked out of its own workbench: %d", w.Code)
	}
	if app.WebRestricted(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("the card should show on a no-auth deployment")
	}
}
