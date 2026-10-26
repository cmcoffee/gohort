package customapps

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every JSON route refuses a signed-out request, answers its owner, and
// refuses a method it does not take. An app's own routes answer nobody it is
// not shared with as if it were not there, and nothing of a disabled app; the
// operator's routes answer nobody but an administrator.
func TestEveryJSONRouteRefusesWhoItShould(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "bob", "pw", false)
	AuthSetUser(auth, "root", "pw", true)
	AuthSetUserApps(auth, "bob", []string{"/apps"})
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id"})
	SaveAppSpec(AppSpec{Slug: "off", Name: "Off", Owner: "alice", RecordKey: "id", Disabled: true})
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
	refused := func(code int) bool {
		return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound || code == http.StatusMethodNotAllowed
	}

	for _, rt := range []struct{ method, path, body, wrong string }{
		{"GET", "/_apps", "", "PUT"},
		{"DELETE", "/_app?slug=nope", "", "GET"},
		{"POST", "/_app/enable?slug=tally", "", "GET"},
		{"POST", "/_app/share?slug=tally&on=false", "", "GET"},
		{"POST", "/_app/share_person?slug=tally&user=bob&on=false", "", "GET"},
		{"POST", "/_app/order", `{"order":[]}`, "GET"},
		{"POST", "/_app/schedule?slug=tally&on=true", "", "GET"},
	} {
		if code := do("", rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do("alice", rt.method, rt.path, rt.body); refused(code) {
			t.Errorf("alice, %s %s answered %d", rt.method, rt.path, code)
		}
		if code := do("alice", rt.wrong, rt.path, ""); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
		}
	}

	for _, rt := range []struct{ method, route, body, wrong string }{
		{"GET", "view-state", "", "PUT"},
		{"POST", "view-state", `{"set":{"a":"1"}}`, ""},
		{"GET", "_settings/values", "", "PUT"},
		{"POST", "_settings/values", `{}`, ""},
		{"POST", "_settings/reset", "", "GET"},
		{"GET", "actions", "", "POST"},
		{"GET", "action-progress/go", "", "POST"},
		{"POST", "action-cancel/go", "", "GET"},
		{"GET", "records", "", "PUT"},
		{"POST", "records", `{"name":"n"}`, ""},
		{"GET", "record?id=nope", "", "PUT"},
		{"DELETE", "record?id=nope", "", ""},
		{"GET", "changes?shared=x&records=y", "", "POST"},
	} {
		path := "/tally/" + rt.route
		if code := do("", rt.method, path, rt.body); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, path, code)
		}
		if code := do("alice", rt.method, path, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed {
			t.Errorf("the owner, %s %s answered %d", rt.method, path, code)
		}
		if code := do("bob", rt.method, path, rt.body); code != http.StatusNotFound {
			t.Errorf("bob, not shared with, %s %s answered %d, want 404", rt.method, path, code)
		}
		if code := do("alice", rt.method, "/off/"+rt.route, rt.body); code != http.StatusForbidden {
			t.Errorf("a disabled app, %s /off/%s answered %d, want 403", rt.method, rt.route, code)
		}
		if rt.wrong != "" {
			if code := do("alice", rt.wrong, path, ""); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d, want 405", rt.wrong, path, code)
			}
		}
	}

	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/_admin/reach?owner=alice&slug=tally", ""},
		{"POST", "/_admin/reach?owner=alice&slug=tally", `{"users":[]}`},
		{"GET", "/_admin/review?owner=alice&slug=tally", ""},
		{"POST", "/_admin/revoke-link?owner=alice&slug=tally", ""},
		{"GET", "/_admin/scripts?owner=alice&slug=tally", ""},
	} {
		for _, who := range []string{"", "alice", "bob"} {
			if code := do(who, rt.method, rt.path, rt.body); code != http.StatusNotFound && code != http.StatusUnauthorized {
				t.Errorf("%q, %s %s answered %d, want it hidden", who, rt.method, rt.path, code)
			}
		}
		if code := do("root", rt.method, rt.path, rt.body); code != http.StatusOK {
			t.Errorf("the administrator, %s %s answered %d", rt.method, rt.path, code)
		}
	}
}
