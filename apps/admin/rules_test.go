package admin

// Global rules are edited under Governance now. The endpoint round-trips the
// list (pasted list markers dropped), and only an administrator reaches it.

import (
	"encoding/json"
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

// Style rules live beside them now. The list is the whole truth: a shipped
// rule whose line is dropped switches off, one that is kept stays on, a
// reworded one becomes an override, and a new line is a custom rule.
func TestGovernanceStyleRulesReplaceTheList(t *testing.T) {
	store := &DBase{Store: kvlite.MemStore()}
	SetPromptOverrideDB(store)
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	shipped := rules.BuiltinStyleRules()
	if len(shipped) < 2 {
		t.Skip("needs two shipped style rules")
	}
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerRulesRoutes(mux)
	post := func(lines ...string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"rules": strings.Join(lines, "\n")})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/style-rules", strings.NewReader(string(body))))
		if w.Code/100 != 2 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
	}
	enabled := func() []string {
		var out []string
		for _, r := range rules.EnabledStyleRules() {
			out = append(out, r.Text)
		}
		return out
	}

	// Keep the first shipped rule, reword the second, drop the rest, add one.
	post(shipped[0].Text, "- "+shipped[1].Text+" Reworded.", "Answer in plain sentences, not bullet lists.")
	got := strings.Join(enabled(), "\n")
	if !strings.Contains(got, shipped[0].Text) || !strings.Contains(got, shipped[1].Text+" Reworded.") ||
		!strings.Contains(got, "Answer in plain sentences") {
		t.Fatalf("the saved list is not what is live:\n%s", got)
	}
	for _, s := range shipped[2:] {
		if strings.Contains(got, s.Text) {
			t.Errorf("a dropped shipped rule is still live: %q", s.Text)
		}
	}
	if n := len(enabled()); n != 3 {
		t.Errorf("want exactly the three saved lines live, got %d", n)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/style-rules", nil))
	if !strings.Contains(w.Body.String(), "Answer in plain sentences") {
		t.Errorf("GET does not return the saved list: %s", w.Body.String())
	}

	// Both lists sit in the one Rules section, each saved to its own endpoint.
	spec, _ := json.Marshal(rulesSection())
	if !strings.Contains(string(spec), "api/global-rules") || !strings.Contains(string(spec), "api/style-rules") {
		t.Errorf("the Rules section should carry both lists: %s", spec)
	}
}
