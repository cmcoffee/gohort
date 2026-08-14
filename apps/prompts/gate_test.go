package prompts

// The admin gate here has never fired.
//
// It read AuthIsAdmin(T.DB, r), and an app's T.DB is
// global.db.Bucket("prompts") — a namespaced substore with no auth
// table. AuthHasUsers(T.DB) was false for the same reason, which
// short-circuited the whole condition, so every authenticated user
// reached a page documented as admin-only. The identical call in
// apps/filestore failed loudly instead (it had no AuthHasUsers guard to
// swallow it), which is how this one was found.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestPromptsGateRefusesANonAdmin(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return root }
	defer func() { AuthDB = prev }()

	root.Set(AuthTable, "user:boss", AuthUser{Username: "boss", Admin: true})
	root.Set(AuthTable, "user:temp", AuthUser{Username: "temp"})

	app := &PromptsApp{}
	// The app's own store is the substore it really gets, so a gate that
	// reads it would refuse everyone — the bug this pins.
	app.DB = root.Bucket("prompts")

	call := func(user string) *httptest.ResponseRecorder {
		token := AuthCreateSession(root, user)
		r := httptest.NewRequest(http.MethodGet, "/prompts/api/list", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		app.adminGated(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("reached"))
		})(w, r)
		return w
	}

	if w := call("temp"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin reached an admin-only page: %d %s", w.Code, w.Body.String())
	}
	if w := call("boss"); w.Code != http.StatusOK {
		t.Errorf("an admin was refused: %d %s", w.Code, w.Body.String())
	}
}

// With no users configured there is no admin to be, and gating on a role
// nobody holds locks a single-user deployment out of its own settings.
func TestPromptsGateStaysOpenWithoutAuth(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return root }
	defer func() { AuthDB = prev }()

	app := &PromptsApp{}
	app.DB = root.Bucket("prompts")

	w := httptest.NewRecorder()
	app.adminGated(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})(w, httptest.NewRequest(http.MethodGet, "/prompts/api/list", nil))
	if w.Code != http.StatusOK {
		t.Errorf("a no-auth deployment was locked out: %d %s", w.Code, w.Body.String())
	}
}
