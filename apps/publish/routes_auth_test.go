package publish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every JSON route Publish serves refuses whoever it should: the deployment
// configuration anyone but an administrator, the rest a signed-out request,
// and a target its owner's alone, so another user is answered as if it were
// not there. A route a method does not take refuses that too.
func TestEveryJSONRouteRefusesWhoItShould(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	mux := http.NewServeMux()
	app.SetWebMux(mux, "")
	app.Routes()
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := do("alice", "POST", "/api/targets", `{"label":"Blog","uses":"api","credential":"blogapi","instructions":"Post it."}`)
	var made struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &made)
	if w.Code != 200 || made.ID == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	one := "/api/targets/" + made.ID

	for _, rt := range []struct {
		method, path, body string
		out, other         int // signed out, and bob (not the owner or not an admin)
	}{
		{"GET", "/api/config", "", 403, 403},
		{"POST", "/api/config", `{}`, 403, 403},
		{"GET", "/api/destinations", "", 401, 200},
		{"GET", "/api/targets", "", 401, 200},
		{"GET", one, "", 401, 404},
		{"GET", one + "/agents", "", 401, 404},
		{"POST", one + "/agents", `{"target":"x","on":true}`, 401, 404},
		{"GET", one + "/options?field=x", "", 401, 404},
		{"DELETE", one, "", 401, 404},
	} {
		if code := do("", rt.method, rt.path, rt.body).Code; code != rt.out {
			t.Errorf("signed out, %s %s answered %d, want %d", rt.method, rt.path, code, rt.out)
		}
		if code := do("bob", rt.method, rt.path, rt.body).Code; code != rt.other {
			t.Errorf("as bob, %s %s answered %d, want %d", rt.method, rt.path, code, rt.other)
		}
	}
	if code := do("root", "GET", "/api/config", "").Code; code != 200 {
		t.Errorf("an administrator reads the configuration: %d", code)
	}
	if code := do("alice", "GET", one, "").Code; code != 200 {
		t.Errorf("the owner reads their target: %d", code)
	}
	for _, wrong := range []struct{ method, path string }{{"PUT", "/api/targets"}, {"POST", one}, {"PUT", one + "/agents"}, {"POST", "/api/destinations"}} {
		if code := do("alice", wrong.method, wrong.path, "{}").Code; code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want 405", wrong.method, wrong.path, code)
		}
	}
	if code := do("alice", "DELETE", one, "").Code; code != http.StatusNoContent {
		t.Errorf("the owner deletes their target: %d", code)
	}
}
