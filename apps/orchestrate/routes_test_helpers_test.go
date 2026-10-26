package orchestrate

import (
	"net/http"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// routed is the app's real HTTP routes, under its sub-mux: the console's and
// the rest. Never Routes(): that runs the boot (migrations, background
// passes, hooks).
func routed(T *OrchestrateApp) http.HandlerFunc {
	return routeMux(T).ServeHTTP
}

// routeMux is routed's mux, for a test that asks which pattern a path meets.
func routeMux(T *OrchestrateApp) *http.ServeMux {
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.registerConsoleRoutes()
	T.registerHandlers()
	return mux
}

// signedUp points AuthDB at a fresh store holding users, so withSession can
// sign a request in as one of them.
func signedUp(t *testing.T, users ...string) {
	t.Helper()
	auth := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	for _, u := range users {
		AuthSetUser(auth, u, "pw", false)
	}
}

// withSession signs r in as user.
func withSession(r *http.Request, user string) *http.Request {
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), user)})
	return r
}

// as signs r in as user, for a test that calls a route rather than a
// handler: against the AuthDB the test set up, or a fresh one when it set
// none, with user added to it if it is not there.
func as(t *testing.T, r *http.Request, user string) *http.Request {
	t.Helper()
	if AuthDB == nil || AuthDB() == nil {
		signedUp(t)
	}
	var u AuthUser
	if !AuthDB().Get(AuthTable, "user:"+user, &u) {
		AuthSetUser(AuthDB(), user, "pw", false)
	}
	return withSession(r, user)
}
