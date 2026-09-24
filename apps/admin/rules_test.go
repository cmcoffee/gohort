package admin

// Global rules are edited under Governance now. The endpoint round-trips the
// list (pasted list markers dropped), and only an administrator reaches it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	rules "github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestGovernanceRulesRoundTripAndAreAdminOnly(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	SetPromptOverrideDB(store)
	t.Cleanup(func() { SetPromptOverrideDB(nil) })

	db := &DBase{Store: kvlite.MemStore()}
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerRulesRoutes(mux)
	call := func(method, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/global-rules", strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	// No users configured: the admin gate passes, as it does on a fresh install.
	if w := call("POST", `{"rules":"- Do not share customer names.\n2. Refuse illegal actions.\n\n"}`, nil); w.Code/100 != 2 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	got := rules.EnabledGlobalRules()
	if len(got) != 2 || got[0].Text != "Do not share customer names." || got[1].Text != "Refuse illegal actions." {
		t.Fatalf("rules not saved as one clean line each: %+v", got)
	}
	if w := call("GET", "", nil); !strings.Contains(w.Body.String(), "Refuse illegal actions.") {
		t.Errorf("GET does not return the saved rules: %s", w.Body.String())
	}

	// With users, a non-admin is refused.
	AuthSetUser(db, "boss", "pw", true)
	AuthSetUser(db, "bob", "pw", false)
	prevAuth := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prevAuth })
	tok := AuthCreateSession(db, "bob")
	if w := call("POST", `{"rules":"nothing"}`, &http.Cookie{Name: "gohort_session", Value: tok}); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin changed the deployment's rules: %d", w.Code)
	}
}
