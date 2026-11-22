package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/userprofile"
)

// What a user profile takes away is gone from the Account page and refused
// at its endpoints, for the people it shapes and no one else; the page says
// which profile shapes them.
func TestAProfileTakesSectionsOffTheAccountPage(t *testing.T) {
	T, sess := accountFixture(t)
	userprofile.SetStore(RootDB)
	t.Cleanup(func() { userprofile.SetStore(nil) })
	if err := userprofile.Save(userprofile.Profile{ID: "desk", Name: "Front Desk", Users: []string{"alice"},
		Removes: []string{userprofile.RemoveAPIKeys, userprofile.RemoveConnectedAccounts, userprofile.RemoveYourData}}); err != nil {
		t.Fatal(err)
	}
	page := func(who string) string {
		w := httptest.NewRecorder()
		T.servePage(w, as(httptest.NewRequest("GET", "/", nil), sess[who]))
		return w.Body.String()
	}
	alice, bob := page("alice"), page("bob")
	for _, gone := range []string{"API keys (personal access)", "Export everything", "api/spending/keys"} {
		if strings.Contains(alice, gone) {
			t.Errorf("alice's page still has %q", gone)
		}
		if !strings.Contains(bob, gone) {
			t.Errorf("bob, whom no profile shapes, lost %q", gone)
		}
	}
	if !strings.Contains(alice, "Your profile: Front Desk") || !strings.Contains(alice, "API keys, Connected accounts, Your data") || strings.Contains(bob, "Your profile") {
		t.Error("the page does not say which profile shapes alice, or says one shapes bob")
	}

	serve := routed(T)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/tokens", ""},
		{"POST", "/api/tokens", `{"name":"laptop"}`},
		{"GET", "/api/token-targets", ""},
		{"GET", "/api/spending/keys", ""},
		{"GET", "/api/connections", ""},
		{"GET", "/oauth/start?cred=x", ""},
		{"GET", "/mcp/connect?server=x", ""},
		{"GET", "/api/artifacts/export?all=1", ""},
		{"POST", "/api/artifacts/import", `{"bundle":"gohort.bundle/v1","artifacts":[]}`},
	} {
		do := func(who string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			serve(w, as(httptest.NewRequest(rt.method, "/account"+rt.path, strings.NewReader(rt.body)), sess[who]))
			return w
		}
		if w := do("alice"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "your profile does not include this") {
			t.Errorf("%s %s for alice: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
		if w := do("bob"); w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "your profile") {
			t.Errorf("%s %s refused bob over a profile that is not his", rt.method, rt.path)
		}
	}
	if len(ListAccountTokens("alice")) != 0 {
		t.Error("a key was made for alice")
	}
}
