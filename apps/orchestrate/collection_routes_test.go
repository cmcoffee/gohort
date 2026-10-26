package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every collection route takes the methods named here and no other, refuses
// a caller who is not signed in, and puts the one write gate in front of
// what it does: a reader the collection was shared with may look but not
// add, a contributor may add but not rename, a stranger finds nothing.
//
// Only reads and requests that are refused are sent, so nothing here
// ingests, researches or calls a model.
func TestEveryCollectionRouteTakesItsMethodsAndKeepsTheGate(t *testing.T) {
	prevRoot, prevAuth, prevBase, prevVec := RootDB, AuthDB, orchestrateBaseDB, VectorDB
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB, VectorDB = prevRoot, prevAuth, prevBase, prevVec })
	RootDB = &DBase{Store: kvlite.MemStore()}
	orchestrateBaseDB = RootDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	signedUp(t, "alice", "bob", "carol", "dave")
	saveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "col-a", Owner: "alice", Name: "Notes",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"carol"}})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}})
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	takes := map[string]string{
		"":                    "GET PATCH DELETE",
		"steward":             "GET POST",
		"paste":               "POST",
		"sources":             "GET",
		"sources/x":           "DELETE",
		"search":              "GET",
		"autofill":            "POST",
		"research":            "POST",
		"audit":               "POST",
		"suggest-description": "POST",
	}
	for action, methods := range takes {
		path := "/api/collections/col-a"
		if action != "" {
			path += "/" + action
		}
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if w := do("alice", m, path, "{}"); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, w.Code)
				}
				continue
			}
			if w := do("", m, path, "{}"); w.Code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, w.Code)
			}
			// A stranger finds no such collection.
			if w := do("dave", m, path, "{}"); w.Code != http.StatusNotFound {
				t.Errorf("dave, %s %s answered %d, want 404", m, path, w.Code)
			}
		}
	}
	for _, action := range []string{"upload", "export"} {
		if w := do("", "GET", "/api/collections/col-a/"+action, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", action, w.Code)
		}
	}
	// The reader looks.
	for _, path := range []string{"/api/collections/col-a", "/api/collections/col-a/sources"} {
		if w := do("bob", "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("bob, a reader, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := do("bob", "GET", "/api/collections/col-a/export", ""); w.Code != http.StatusOK {
		t.Errorf("bob, a reader, could not export: %d %s", w.Code, w.Body.String())
	}
	// The reader does not add, and the contributor gets past the gate to the
	// paste's own refusal of an empty document.
	paste := `{"title":"t","text":""}`
	if w := do("bob", "POST", "/api/collections/col-a/paste", paste); w.Code != http.StatusForbidden {
		t.Errorf("bob, a reader, pasted: %d %s", w.Code, w.Body.String())
	}
	if w := do("carol", "POST", "/api/collections/col-a/paste", paste); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too short") {
		t.Errorf("carol, a contributor, was stopped before the paste: %d %s", w.Code, w.Body.String())
	}
	if w := do("bob", "POST", "/api/collections/col-a/upload", `{"name":""}`); w.Code != http.StatusForbidden {
		t.Errorf("bob, a reader, uploaded: %d %s", w.Code, w.Body.String())
	}
	// Neither of them renames it, re-scopes it, deletes from it or names its
	// curator.
	for _, u := range []string{"bob", "carol"} {
		for _, c := range []struct{ method, path, body string }{
			{"PATCH", "/api/collections/col-a", `{"name":"Mine now"}`},
			{"DELETE", "/api/collections/col-a", ""},
			{"DELETE", "/api/collections/col-a/sources/x", ""},
			{"POST", "/api/collections/col-a/steward", `{"curator_agent":"x"}`},
		} {
			if w := do(u, c.method, c.path, c.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s answered %d, want 403", u, c.method, c.path, w.Code)
			}
		}
	}
	if got, ok := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a"); !ok || got.Name != "Notes" {
		t.Errorf("the collection changed: %+v %v", got, ok)
	}
}

// Asking for deployment scope is not getting it. The owner's request is filed
// for an administrator and the collection stays theirs; an administrator's
// own promotes at once; nobody but the owner asks.
func TestWideningACollectionGoesThroughAnAdministrator(t *testing.T) {
	prevRoot, prevAuth, prevBase, prevVec := RootDB, AuthDB, orchestrateBaseDB, VectorDB
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB, VectorDB = prevRoot, prevAuth, prevBase, prevVec })
	RootDB = &DBase{Store: kvlite.MemStore()}
	orchestrateBaseDB = RootDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	signedUp(t, "alice", "carol")
	AuthSetUser(AuthDB(), "root", "pw", true)
	saveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "col-a", Owner: "alice", Name: "Notes",
		AllowedUsers: []string{"carol"}, Contributors: []string{"carol"}})
	saveCollection(UserDB(CollectionsDB(), "root"), Collection{ID: "col-r", Owner: "root", Name: "Shared"})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}})
	widen := func(user, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, withSession(httptest.NewRequest("PATCH", "/api/collections/"+id, strings.NewReader(`{"scope":"deployment"}`)), user))
		return w
	}
	if w := widen("carol", "col-a"); w.Code != http.StatusForbidden {
		t.Errorf("a contributor changed the scope: %d %s", w.Code, w.Body.String())
	}
	if w := widen("alice", "col-a"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requested":true`) {
		t.Errorf("the owner's ask: %d %s", w.Code, w.Body.String())
	}
	if c, ok := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a"); !ok || IsDeploymentScope(c) {
		t.Errorf("asking promoted the collection: %+v %v", c, ok)
	}
	if w := widen("root", "col-r"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requested":false`) {
		t.Errorf("an administrator's own: %d %s", w.Code, w.Body.String())
	}
}
