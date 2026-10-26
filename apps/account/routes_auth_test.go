package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every JSON route Account serves refuses a signed-out request, answers a
// signed-in one (whatever the answer, it is not a refusal of who is asking),
// and refuses a method it does not take. Each route acts only on the
// signed-in user's own account, so there is no other user's record to reach.
func TestEveryJSONRouteRefusesTheSignedOut(t *testing.T) {
	T, sess := accountFixture(t)
	serve := routed(T)
	for _, rt := range []struct{ method, path, body, wrong string }{
		{"GET", "/api/prefs", "", "PUT"},
		{"POST", "/api/prefs", `{"notify":true}`, ""},
		{"POST", "/api/password", `{"current":"x","new":"short"}`, "GET"},
		{"GET", "/api/connections", "", "PUT"},
		{"POST", "/api/connections", `{"name":"nope"}`, ""},
		{"GET", "/api/tokens", "", "PUT"},
		{"POST", "/api/tokens", `{"name":"laptop"}`, ""},
		{"POST", "/api/tokens?action=scope&id=nope", `{"scope":{}}`, ""},
		{"DELETE", "/api/tokens?id=nope", "", ""},
		{"GET", "/api/token-targets", "", "POST"},
		{"POST", "/api/artifacts/preview", `{"bundle":"gohort.bundle/v1","artifacts":[]}`, "GET"},
		{"POST", "/api/artifacts/import", `{"bundle":"gohort.bundle/v1","artifacts":[]}`, "GET"},
	} {
		do := func(method, token string) int {
			r := httptest.NewRequest(method, "/account"+rt.path, strings.NewReader(rt.body))
			if token != "" {
				r = as(r, token)
			}
			w := httptest.NewRecorder()
			serve(w, r)
			return w.Code
		}
		if code := do(rt.method, ""); code != http.StatusUnauthorized {
			t.Errorf("signed out, %s %s answered %d, want 401", rt.method, rt.path, code)
		}
		if code := do(rt.method, sess["alice"]); code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusMethodNotAllowed {
			t.Errorf("signed in, %s %s answered %d", rt.method, rt.path, code)
		}
		if rt.wrong != "" {
			if code := do(rt.wrong, sess["alice"]); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s answered %d, want 405", rt.wrong, rt.path, code)
			}
		}
	}
}
