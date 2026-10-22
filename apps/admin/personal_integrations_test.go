package admin

// The Governance tab's Personal integrations: an administrator reads and sets
// whether people add their own API credentials and MCP servers; nobody else
// can, and a value that is not a policy is refused.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAnAdministratorSetsThePersonalIntegrationPolicies(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "dana", "pw", false)
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	do := func(who, method, body string) (int, map[string]string) {
		t.Helper()
		r := httptest.NewRequest(method, "/api/personal-integrations", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, who)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out map[string]string
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, out := do("root", "GET", ""); code != 200 || out["credentials"] != "allowed" || out["mcp_servers"] != "approval" {
		t.Fatalf("defaults: %d %v", code, out)
	}
	if code, out := do("root", "POST", `{"credentials":"admin","mcp_servers":"allowed"}`); code != 200 || out["credentials"] != "admin" || out["mcp_servers"] != "allowed" {
		t.Fatalf("set: %d %v", code, out)
	}
	if governance.Of(governance.Credentials) != governance.AdminOnly {
		t.Error("the setting did not take")
	}
	if code, _ := do("root", "POST", `{"credentials":"whenever"}`); code != 400 {
		t.Errorf("a value that is not a policy: %d", code)
	}
	if code, _ := do("dana", "POST", `{"credentials":"allowed"}`); code < 400 {
		t.Errorf("someone who is not an administrator changed it: %d", code)
	}
	if governance.Of(governance.Credentials) != governance.AdminOnly {
		t.Error("a refused change took effect")
	}
}
