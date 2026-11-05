package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A maintainer does what a contributor does and curates besides; the
// collection itself (its name, who it reaches, its curator agent, deleting it)
// stays the owner's.
func TestACollectionMaintainerCuratesButDoesNotOwn(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob", "carol"}, Maintainers: []string{"bob"}}
	for _, a := range []struct{ action, method string }{
		{"upload", "POST"}, {"sources", "DELETE"}, {"autofill", "POST"},
		{"suggestions", "POST"}, {"audit", "POST"}, {"", "PATCH"},
	} {
		if why := collectionWriteRefusal(c, "bob", a.action, a.method, false); why != "" {
			t.Errorf("a maintainer was refused %s %q: %s", a.method, a.action, why)
		}
	}
	for _, a := range []struct{ action, method string }{{"", "DELETE"}, {"steward", "POST"}} {
		if collectionWriteRefusal(c, "bob", a.action, a.method, false) == "" {
			t.Errorf("a maintainer may %s %q", a.method, a.action)
		}
	}
	if collectionWriteRefusal(c, "carol", "suggestions", "POST", false) == "" {
		t.Error("a reader may review suggestions")
	}
	// Held to the share, like a contributor: off it, a maintainer is nothing.
	c.AllowedUsers = []string{"carol"}
	if CollectionMaintainer(c, "bob") {
		t.Error("somebody it is no longer shared with is still a maintainer")
	}
	if CollectionMaintainer(Collection{Owner: "alice", Maintainers: []string{"alice"}, AllowedUsers: []string{"alice"}}, "alice") {
		t.Error("the owner counted as a maintainer of their own collection")
	}
}

// Through the routes: the owner names a maintainer (which shares it with
// them), the maintainer edits what it takes in on the owner's record, is
// refused its name and its reach, and taking them off the share ends it.
func TestACollectionMaintainerThroughTheRoutes(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot, prevVec, prevBase := AuthDB, RootDB, VectorDB, orchestrateBaseDB
	AuthDB, RootDB, VectorDB = func() Database { return adb }, root, &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{AppCore: AppCore{DB: root.Bucket(CollectionsBucket)}}
	orchestrateBaseDB = app.DB
	t.Cleanup(func() { AuthDB, RootDB, VectorDB, orchestrateBaseDB = prevAuth, prevRoot, prevVec, prevBase })
	for _, u := range []string{"alice", "bob", "carol"} {
		adb.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	SaveCollection(UserDB(app.DB, "alice"), Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"carol"}})
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	if w := as("bob", "PATCH", "/api/collections/col-1", `{"maintainers":["bob"]}`); w.Code < 400 {
		t.Fatalf("somebody made themselves a maintainer: %d", w.Code)
	}
	if w := as("alice", "PATCH", "/api/collections/col-1", `{"maintainers":["bob"]}`); w.Code != 200 {
		t.Fatalf("the owner could not name a maintainer: %d %s", w.Code, w.Body.String())
	}
	got, _ := LoadCollection(UserDB(app.DB, "alice"), "alice", "col-1")
	if strings.Join(got.Maintainers, ",") != "bob" || !strings.Contains(strings.Join(got.AllowedUsers, ","), "bob") || !strings.Contains(strings.Join(got.AllowedUsers, ","), "carol") {
		t.Fatalf("after naming a maintainer: %+v", got)
	}
	if w := as("bob", "PATCH", "/api/collections/col-1", `{"description":"How we run things","filter_rules":"- only runbooks"}`); w.Code != 200 {
		t.Fatalf("the maintainer could not curate: %d %s", w.Code, w.Body.String())
	}
	got, _ = LoadCollection(UserDB(app.DB, "alice"), "alice", "col-1")
	if got.Description != "How we run things" || got.FilterRules != "- only runbooks" || got.Owner != "alice" {
		t.Errorf("the owner's record after the maintainer's edit: %+v", got)
	}
	var fork Collection
	if UserDB(app.DB, "bob").Get(CollectionsTable, "col-1", &fork) {
		t.Error("the maintainer's edit made a copy in their own store")
	}
	for _, body := range []string{`{"name":"Mine"}`, `{"allowed_users":["bob"]}`, `{"contributors":["bob"]}`, `{"scope":"deployment"}`} {
		if w := as("bob", "PATCH", "/api/collections/col-1", body); w.Code != http.StatusForbidden {
			t.Errorf("a maintainer's %s answered %d, want 403", body, w.Code)
		}
	}
	if w := as("bob", "DELETE", "/api/collections/col-1", ""); w.Code != http.StatusForbidden {
		t.Errorf("a maintainer's delete answered %d", w.Code)
	}
	if w := as("bob", "GET", "/api/collections/col-1/suggestions", ""); w.Code != 200 {
		t.Errorf("the maintainer cannot see the suggestions: %d %s", w.Code, w.Body.String())
	}
	if w := as("carol", "GET", "/api/collections/col-1/suggestions", ""); w.Code != http.StatusForbidden {
		t.Errorf("a reader sees the suggestions: %d", w.Code)
	}
	// Taken off the share, off the maintainers too.
	if w := as("alice", "PATCH", "/api/collections/col-1", `{"allowed_users":["carol"]}`); w.Code != 200 {
		t.Fatalf("unshare: %d", w.Code)
	}
	if got, _ := LoadCollection(UserDB(app.DB, "alice"), "alice", "col-1"); len(got.Maintainers) != 0 {
		t.Errorf("a maintainer outlived the share: %v", got.Maintainers)
	}
	if w := as("bob", "PATCH", "/api/collections/col-1", `{"description":"x"}`); w.Code < 400 {
		t.Errorf("a former maintainer still curates: %d", w.Code)
	}
}
