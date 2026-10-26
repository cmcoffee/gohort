package extensions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Every JSON route on jsonapi refuses a signed-out request, answers a
// signed-in one (whatever the answer, it is not a refusal of who is asking),
// and refuses a method it does not take. A credential or a tool is the
// owner's: another user asking for it by name is told there is no such thing.
func TestEveryJSONRouteRefusesTheSignedOut(t *testing.T) {
	app, req := authedExtensions(t)
	h := serve(app)
	adb := AuthDB()
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	bob := AuthCreateSession(adb, "bob")
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "rt_auth_tool", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}

	for _, rt := range []struct{ method, path, body, wrong string }{
		{"GET", "/api/credentials", "", "PUT"},
		{"POST", "/api/credentials", `{"name":"rt_auth_alice","type":"bearer","base_url":"https://a.test","secret":"k"}`, ""},
		{"POST", "/api/credentials?action=disable&name=rt_auth_alice", "", ""},
		{"DELETE", "/api/credentials?name=rt_auth_nope", "", ""},
		{"GET", "/api/mcp-servers", "", "PUT"},
		{"POST", "/api/mcp-servers", `{"name":"rt_auth"}`, ""},
		{"DELETE", "/api/mcp-servers?name=rt_auth_nope", "", ""},
		{"GET", "/api/category-security?category=nope&section=run", "", "PUT"},
		{"POST", "/api/category-security?category=nope&section=run", `{"target":"x","on":true}`, ""},
		{"GET", "/api/tools", "", "PUT"},
		{"POST", "/api/tools?action=share&name=rt_auth_tool", `{"shared_with":[]}`, ""},
		{"POST", "/api/tools?action=disable&name=rt_auth_tool", "", ""},
		{"DELETE", "/api/tools?name=rt_auth_nope", "", ""},
		{"GET", "/api/tool-access?section=run&name=rt_auth_tool", "", "PUT"},
		{"POST", "/api/tool-access?section=run&name=rt_auth_tool", `{"target":"ask_first","on":false}`, ""},
		{"GET", "/api/user-candidates", "", "POST"},
		{"GET", "/api/skill-tools", "", "POST"},
		{"GET", "/api/skill-collections", "", "POST"},
	} {
		do := func(method, token string) int {
			r := httptest.NewRequest(method, "/extensions"+rt.path, strings.NewReader(rt.body))
			if token != "" {
				r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
			}
			w := httptest.NewRecorder()
			h(w, r)
			return w.Code
		}
		if code := do(rt.method, ""); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		w := httptest.NewRecorder()
		r := req(rt.method, rt.path)
		r.Body = readCloser(rt.body)
		h(w, r)
		if code := w.Code; code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed {
			t.Errorf("signed in, %s %s answered %d", rt.method, rt.path, code)
		}
		if rt.wrong != "" {
			if code := do(rt.wrong, bob); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
			}
		}
	}

	// Alice's credential, asked for by bob, is not there; switching or
	// deleting it by name reaches only bob's own namespace, and leaves
	// alice's as it was.
	as := func(method, path string) int {
		r := httptest.NewRequest(method, "/extensions"+path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: bob})
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if code := as("GET", "/api/credentials?name=rt_auth_alice"); code != http.StatusNotFound {
		t.Errorf("bob read alice's credential: %d, want 404", code)
	}
	if code := as("GET", "/api/tools?name=rt_auth_tool"); code != http.StatusNotFound {
		t.Errorf("bob read alice's tool: %d, want 404", code)
	}
	as("POST", "/api/credentials?action=enable&name=rt_auth_alice")
	as("DELETE", "/api/credentials?name=rt_auth_alice")
	cred, found := Secure().LoadUser("alice", "rt_auth_alice")
	if !found || !cred.Disabled {
		t.Fatalf("bob's requests changed alice's credential: found=%v %+v", found, cred)
	}
	Secure().DeleteUser("alice", "rt_auth_alice")
}
