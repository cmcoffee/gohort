package orchestrate

// Who the admin gate is for, now that it is not for the whole app.
//
// It was wrapped around all sixty routes, on the posture that administrators
// build agents and end users consume them. A user owns their agents in the
// same way they own their tools and their collections, so that gate kept them
// out of their own things and this session's entire sharing model out of reach
// of the people it is written for.
//
// What remains behind it is the CONSOLE: one administrator's view across every
// user's agents, runs and bridges. That is genuinely the deployment's rather
// than one person's, and it keeps the gate route by route — which is the
// granularity this always wanted, rather than a flag over an app.

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

func TestTheAdminGateGuardsTheConsoleNotTheApp(t *testing.T) {
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
	// The refusal says what THIS is and where their own are. The old wording,
	// "Agents is admin-only", told somebody what they were not — in an app
	// they had just been sent to, about records they own.
	w := reached(sessionReq(t, root, "temp"))
	body := w.Body.String()
	if !strings.Contains(body, "administrator") || !strings.Contains(strings.ToLower(body), "your own") {
		t.Errorf("the refusal does not say what it is or where theirs are: %s", body)
	}

	// The app itself is not hidden any more. Hiding it was the same decision
	// as the blanket gate and had the same consequence: a user with agents,
	// tools and collections of their own could not see the place they live.
	if app.WebRestricted(sessionReq(t, root, "temp")) {
		t.Error("the app is hidden from a user who owns agents in it")
	}
	if app.WebRestricted(sessionReq(t, root, "boss")) {
		t.Error("the app is hidden from an admin")
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

// What the console gate is left guarding, and what it must not creep back onto.
//
// "Console" names two different things that were behind one gate: somebody's
// own operator view — their agents, runs, monitors, approvals, goals, broken
// tools — and one view ACROSS owners. The first is theirs and the landing page
// is built from it, which is why sixty-one calls on that page 403'd for every
// non-admin. The second is the administrator's and keeps the gate.
func TestTheConsoleGateIsOnlyOnTheCrossOwnerViews(t *testing.T) {
	src, err := os.ReadFile("console.go")
	if err != nil {
		t.Fatalf("reading the console routes: %v", err)
	}
	gated := regexp.MustCompile(`T\.HandleFunc\("([^"]+)",\s*gw?\(`)
	for _, m := range gated.FindAllStringSubmatch(string(src), -1) {
		if !strings.Contains(m[1], "/bridge") {
			t.Errorf("%s is admin-gated but is not a cross-owner view; a user's own operator "+
				"surface is built from these and 403s without them", m[1])
		}
	}
	// And the cross-owner one keeps it: it lists every owner's bridges on
	// purpose, which is the one thing here that is the deployment's.
	if !strings.Contains(string(src), `T.HandleFunc("/api/console/bridges", g(`) {
		t.Error("the cross-owner bridges listing lost its admin gate")
	}
	// A mutating action that lost the gate must keep the METHOD guard. The two
	// travelled together and only one of them was an ACL: dropping both would
	// take a CSRF protection with it, since these endpoints are query-param
	// driven and SameSite=Lax still sends the cookie on a top-level GET.
	for _, action := range []string{"/api/console/agents/delete", "/api/console/monitors/pause"} {
		if !strings.Contains(string(src), `T.HandleFunc("`+action+`", w(`) {
			t.Errorf("%s no longer rejects safe methods", action)
		}
	}
}
