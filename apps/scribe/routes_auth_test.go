package scribe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every JSON route refuses a signed-out request, answers the guide's owner,
// and refuses a method it does not take. A guide's routes answer nobody it is
// not shared with as if it were not there, and a change from someone it is
// shared with only to read is refused.
func TestEveryJSONRouteRefusesWhoItShould(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	for _, u := range []string{"alice", "bob", "carol"} {
		auth.Set(AuthTable, "user:"+u, AuthUser{Username: u, Apps: []string{T.WebPath()}})
	}
	audb := UserDB(T.DB, "alice")
	g := saveGuideRev(audb, Guide{ID: "g1", Title: "Runbook", Owner: "alice", Sections: []Section{
		{ID: "s1", Title: "One", Markdown: "first", Order: 1}, {ID: "s2", Title: "Two", Markdown: "second", Order: 2}}}, "Created")
	g.applyShare("alice", guideShare{AllowedUsers: []string{"carol"}})
	saveGuide(audb, g)
	indexGuideShares(T.DB, "alice", g.ID, g.Shared, g.AllowedUsers)
	h := routed(T)
	do := func(user, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	refusal := func(code int) bool {
		return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound || code == http.StatusMethodNotAllowed
	}

	// Routes with no guide of their own.
	for _, rt := range []struct{ method, path, body, wrong string }{
		{"GET", "/guides", "", "PUT"},
		{"POST", "/new", `{"title":"Fresh"}`, "GET"},
		{"POST", "/chat/active", `{"id":"g1"}`, "GET"},
		{"GET", "/share/candidates?id=g1", "", "PUT"},
		{"GET", "/import/page", "", "PUT"},
		{"GET", "/curator/runs", "", "PUT"},
		{"GET", "/curator/pending", "", "PUT"},
		{"POST", "/curator/undo", `{"run_id":"r","finding_id":"f"}`, "GET"},
		{"POST", "/curator/run", "", "GET"},
	} {
		if code := do("", rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("alice", rt.method, rt.path, rt.body); refusal(code) {
			t.Errorf("alice, %s %s answered %d", rt.method, rt.path, code)
		}
		if code := do("alice", rt.wrong, rt.path, ""); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
		}
	}

	// One guide's routes. write marks a change, which a reader may not make;
	// gone marks a route that answers an unknown guide with an empty reply
	// rather than 404.
	for _, rt := range []struct {
		method, path, body, wrong string
		write, gone               bool
	}{
		{"GET", "/guide?id=g1", "", "PUT", false, false},
		{"GET", "/settings?id=g1", "", "PUT", false, false},
		{"POST", "/settings?id=g1", `{"title":"Runbook"}`, "", true, false},
		{"GET", "/share?id=g1", "", "PUT", true, false},
		{"PATCH", "/share?id=g1", `{"allowed_users":["carol"]}`, "", true, false},
		{"POST", "/body?id=g1", `{"markdown":"## One\nfirst\n\n## Two\nsecond"}`, "GET", true, false},
		{"GET", "/image?id=g1", "", "PUT", false, false},
		{"POST", "/image?id=g1", `{"url":"https://img.example/x.png"}`, "", true, false},
		{"GET", "/revisions?id=g1", "", "PUT", false, true},
		{"POST", "/restore?id=g1&rev=nope", "", "GET", true, false},
		{"GET", "/section?guide=g1&section=s1", "", "PUT", false, false},
		{"POST", "/section?guide=g1&section=s1", `{"title":"One","markdown":"first"}`, "", true, false},
		{"POST", "/section/move?guide=g1&section=s1&dir=down", "", "GET", true, false},
		{"POST", "/section/add?guide=g1", `{"title":"Three"}`, "GET", true, false},
		{"POST", "/collections?guide=g1", `{"collections":[]}`, "", true, false},
		{"POST", "/references?guide=g1", `{"references":[]}`, "", true, false},
		{"POST", "/sources/update?id=g1", "", "GET", true, false},
		{"GET", "/publish/state?id=g1", "", "PUT", false, false},
		{"GET", "/publish/job?id=g1", "", "PUT", false, false},
		{"POST", "/publish/to?id=g1", `{"kind":"nowhere"}`, "GET", true, false},
		{"POST", "/publish/again?id=g1&kind=nowhere", "", "GET", true, false},
		{"POST", "/publish/stop?id=g1", "", "GET", true, false},
		{"POST", "/publish/keep?id=g1", `{"on":false}`, "GET", true, false},
		{"POST", "/publish/link?id=g1", `{"kind":"nowhere","link":"x"}`, "GET", true, false},
	} {
		if code := do("", rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("alice", rt.method, rt.path, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed {
			t.Errorf("the owner, %s %s answered %d", rt.method, rt.path, code)
		}
		if code := do("bob", rt.method, rt.path, rt.body); !rt.gone && code != http.StatusNotFound {
			t.Errorf("bob, not shared with, %s %s answered %d, want 404", rt.method, rt.path, code)
		}
		if code := do("carol", rt.method, rt.path, rt.body); rt.write && code != http.StatusForbidden {
			t.Errorf("carol, a reader, %s %s answered %d, want 403", rt.method, rt.path, code)
		}
		if rt.wrong != "" {
			if code := do("alice", rt.wrong, rt.path, ""); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
			}
		}
	}
	// A job's routes, which answer anyone but the person it runs for as if
	// there were none.
	for _, rt := range []struct{ method, path string }{
		{"GET", "/import/page/job?job=nope"},
		{"POST", "/import/page/stop?job=nope"},
		{"POST", "/distill/stop?id=nope"},
	} {
		if code := do("", rt.method, rt.path, ""); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("alice", rt.method, rt.path, ""); code != http.StatusNotFound {
			t.Errorf("no such job, %s %s answered %d, want 404", rt.method, rt.path, code)
		}
	}
	if code := do("bob", "DELETE", "/guide?id=g1", ""); code != http.StatusNotFound {
		t.Errorf("bob deleting alice's guide answered %d, want 404", code)
	}
	if code := do("carol", "DELETE", "/guide?id=g1", ""); code != http.StatusForbidden {
		t.Errorf("carol deleting alice's guide answered %d, want 403", code)
	}
	if _, ok := loadGuide(audb, "g1"); !ok {
		t.Fatal("the guide is gone")
	}
}
