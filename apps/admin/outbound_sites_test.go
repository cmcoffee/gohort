package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The administrator's site list through the real routes: only an
// administrator reaches it; adding keeps only the site and refuses what
// cannot be one; the row's control switches Always allow and Never; Ask
// first is not a choice at this level; Remove takes it off. Each change is
// the one the outbound check reads.
func TestTheAdminSiteListThroughItsRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	if code, _ := do("alice", "POST", "/api/outbound-sites", `{"site":"paste.example","value":"block"}`); code < 400 || AdminSites.Policy(RootDB, "paste.example") != "" {
		t.Fatalf("a user who is not an administrator added a site: %d", code)
	}
	if code, body := do("root", "POST", "/api/outbound-sites", `{"site":"https://Paste.Example/raw/abc","value":"block"}`); code >= 300 || AdminSites.Policy(RootDB, "paste.example") != PolicyBlock {
		t.Fatalf("add: %d %s", code, body)
	}
	for _, bad := range []string{`{"site":"192.168.1.5","value":"block"}`, `{"site":"paste.example","value":"ask"}`} {
		if code, _ := do("root", "POST", "/api/outbound-sites", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if code, body := do("root", "GET", "/api/outbound-sites", ""); code != 200 || !strings.Contains(body, `"host":"paste.example"`) || !strings.Contains(body, `"_policy":"block"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := do("root", "POST", "/api/outbound-sites/policy?host=paste.example", `{"_policy":"allow"}`); code >= 300 || AdminSites.Policy(RootDB, "paste.example") != PolicyAllow {
		t.Fatalf("switch to Always allow: %d %s", code, body)
	}
	if code, _ := do("root", "POST", "/api/outbound-sites/policy?host=other.example", `{"_policy":"block"}`); code != http.StatusNotFound {
		t.Errorf("switching a site not on the list: %d, want 404", code)
	}
	if code, body := do("root", "DELETE", "/api/outbound-sites?host=paste.example", ""); code >= 300 || AdminSites.Policy(RootDB, "paste.example") != "" {
		t.Fatalf("remove: %d %s", code, body)
	}
}

// The list is a rule of the deployment's for everyone, so it sits on the
// Oversight tab with the others, not among the Tuning knobs.
func TestTheAdminSiteListIsOnOversight(t *testing.T) {
	found := false
	for _, s := range (&AdminApp{}).governanceSections() {
		if s.Title == "Sites agents may send to" {
			found = true
		}
	}
	if !found || sectionGroup["Sites agents may send to"] != "Oversight" {
		t.Errorf("the site list is not on the Oversight tab (built %v, tab %q)", found, sectionGroup["Sites agents may send to"])
	}
	for _, c := range tuningExtraPanes["Sign-in & security"] {
		if sub, ok := c.(ui.Subsection); ok && strings.Contains(sub.Title, "Sites agents may send to") {
			t.Error("the site list is still among the Tuning knobs too")
		}
	}
}
