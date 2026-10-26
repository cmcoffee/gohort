package knowledge

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Knowledge's one JSON route refuses a signed-out request, answers a signed-in
// one with the people it may share with, and refuses a method it does not
// take.
func TestTheJSONRouteRefusesTheSignedOut(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	app := &KnowledgeApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	mux := http.NewServeMux()
	app.SetWebMux(mux, "")
	app.Routes()
	do := func(user, method string) int {
		r := httptest.NewRequest(method, "/api/user-candidates", nil)
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := do("", "GET"); code != http.StatusUnauthorized {
		t.Errorf("signed out answered %d, want 401", code)
	}
	if code := do("alice", "GET"); code != http.StatusOK {
		t.Errorf("signed in answered %d, want 200", code)
	}
	if code := do("alice", "POST"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST answered %d, want 405", code)
	}
}
