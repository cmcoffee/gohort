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
	"github.com/cmcoffee/gohort/core/mcppersonal"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/webhooks"
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

// An administrator sees every person's servers, with what each offered, and
// approves or holds them.
func TestAnAdministratorApprovesAPersonalMCPServer(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "dana", "pw", false)
	if err := mcppersonal.Save(mcppersonal.Server{Owner: "dana", Name: "linear", URL: "https://mcp.invalid/mcp"}, "", true); err != nil {
		t.Fatal(err)
	}
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	do := func(who, method, path string) (int, string) {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, who)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := do("root", "GET", "/api/personal-mcp-servers")
	if code != 200 || !strings.Contains(body, `"owner":"dana"`) || !strings.Contains(body, `"awaiting":true`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, _ := do("dana", "POST", "/api/personal-mcp-servers?action=enable&owner=dana&name=linear"); code < 400 {
		t.Error("the owner approved their own server through the admin endpoint")
	}
	if code, _ := do("root", "POST", "/api/personal-mcp-servers?action=enable&owner=dana&name=linear"); code >= 300 {
		t.Fatalf("approve: %d", code)
	}
	if s, _ := mcppersonal.Load("dana", "linear"); !s.Usable() {
		t.Errorf("approval should switch it on: %+v", s)
	}
}

// The administrator's list of personal Teams bots: every person's, the one
// waiting approved from here, switched off, and deleted with its webhook;
// nobody else acts on it.
func TestAnAdministratorApprovesTeamsBots(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "dana", "pw", false)
	webhooks.LinkedServices = func(owner string) []string { return []string{"teams"} }
	webhooks.RegisterChannelCheck(teamsbots.VerifyName, func(*http.Request, webhooks.Webhook, []byte) (int, string, error) { return 200, "", nil })
	t.Cleanup(func() { webhooks.LinkedServices = nil })
	b, err := teamsbots.Save(teamsbots.Bot{Owner: "dana", Name: "Wren", AppID: "11111111-2222-3333-4444-555555555555"}, "s", true)
	if err != nil {
		t.Fatal(err)
	}
	h, err := webhooks.Create(webhooks.Webhook{Label: "Wren", Owner: "dana", Verify: teamsbots.VerifyName, Feeds: "channel:teams"}, "")
	if err != nil {
		t.Fatal(err)
	}
	teamsbots.SetWebhook("dana", b.Service, h.Name)
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	do := func(who, method, path string) (int, string) {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, who)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if _, list := do("root", "GET", "/api/personal-teams-bots"); !strings.Contains(list, `"owner":"dana"`) || !strings.Contains(list, `"awaiting":true`) ||
		!strings.Contains(list, "multi-tenant") || strings.Contains(list, `"s"`) {
		t.Errorf("the list: %s", list)
	}
	if code, _ := do("dana", "POST", "/api/personal-teams-bots?action=enable&owner=dana&service=teams"); code < 400 {
		t.Errorf("the owner approved their own: %d", code)
	}
	if code, _ := do("root", "POST", "/api/personal-teams-bots?action=enable&owner=dana&service=teams"); code >= 300 {
		t.Errorf("approve: %d", code)
	}
	if got, _ := teamsbots.Load("dana", "teams"); !got.Usable() {
		t.Error("not approved")
	}
	do("root", "POST", "/api/personal-teams-bots?action=disable&owner=dana&service=teams")
	if got, _ := teamsbots.Load("dana", "teams"); got.Usable() {
		t.Error("not switched off")
	}
	if code, _ := do("root", "POST", "/api/personal-teams-bots?action=wave&owner=dana&service=teams"); code != 400 {
		t.Errorf("an action that is not one: %d", code)
	}
	if code, _ := do("root", "DELETE", "/api/personal-teams-bots?owner=dana&service=teams"); code >= 300 {
		t.Errorf("delete: %d", code)
	}
	if _, ok := webhooks.Get(h.Name); ok {
		t.Error("its webhook outlived it")
	}
}
