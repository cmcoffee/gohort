package admin

// Admin › Tools reads and changes every user's tools: the pending queue, the
// shared pool, the tools scoped to agents, and the orphans whose agent was
// deleted. These pin what each request answers and what each change does.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// agentRow is the part of an agent record the tools page reads; gob matches
// fields by name, so it stands in for the real one.
type agentRow struct {
	ID      string
	Name    string
	Hidden  bool
	OwnedBy string
}

type toolsFixture struct {
	a     *AdminApp
	mux   *http.ServeMux
	token string
}

func newToolsFixture(t *testing.T) *toolsFixture {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(db, "root", "pw", true)
	AuthSetUser(db, "bob", "pw", false)
	f := &toolsFixture{a: &AdminApp{db: db}, mux: http.NewServeMux(), token: AuthCreateSession(db, "root")}
	f.a.registerToolsRoutes(f.mux)
	return f
}

func (f *toolsFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: f.token})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *toolsFixture) seedBob(t *testing.T) {
	t.Helper()
	db := f.a.db
	db.Set("pending_temp_tools", "bob", []PendingTempTool{{Tool: TempTool{Name: "waiting_tool", Description: "d", CommandTemplate: "echo w"}, RequestedAt: time.Now()}})
	if err := AdminPersistTempTool(db, "bob", TempTool{Name: "pool_tool", Description: "d", CommandTemplate: "echo p", Credential: "no_such_cred"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(db, "bob", TempTool{Name: "scoped_tool", Description: "d", CommandTemplate: "echo s"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(db, "bob", TempTool{Name: "child_tool", Description: "d", CommandTemplate: "echo c"}); err != nil {
		t.Fatal(err)
	}
	udb := UserDB(db.Bucket("orchestrate"), "bob")
	for _, ag := range []agentRow{
		{ID: "a-1", Name: "Scout"}, {ID: "a-2", Name: "Helper"},
		{ID: "a-hidden", Name: "Guide Author", Hidden: true}, {ID: "a-sub", Name: "Child", OwnedBy: "a-1"},
	} {
		udb.Set("orchestrate_agents", ag.ID, ag)
	}
	SetUserToolScopeAgents(db, "bob", "scoped_tool", []string{"a-1", "a-hidden", "a-gone", "a-2"})
	SetUserToolScopeAgents(db, "bob", "child_tool", []string{"a-sub"})
	AddOrphanedTempTools(db, "bob", []OrphanedTempTool{{Tool: TempTool{Name: "lost_tool", Description: "d"}, FormerAgentName: "Gone", OrphanedAt: time.Now()}})
}

func TestTheToolsPageListsEveryUsersTools(t *testing.T) {
	f := newToolsFixture(t)
	f.seedBob(t)
	w := f.do(t, "GET", "/api/persistent-tools", "")
	if w.Code != 200 {
		t.Fatalf("GET answered %d %s", w.Code, w.Body.String())
	}
	var got map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if p := got["pending"]; len(p) != 1 || p[0]["owner"] != "bob" {
		t.Errorf("the pending queue should hold bob's waiting tool: %+v", p)
	}
	if a := got["active"]; len(a) != 1 || a[0]["owner"] != "bob" || a[0]["has_missing"] != true {
		t.Errorf("the pool should hold bob's shared tool, flagged for its missing credential: %+v", a)
	}
	b := got["bundled"]
	if len(b) != 1 || b[0]["agent"] != "Scout, Helper" {
		t.Fatalf("the scoped tool should be one row naming only its visible agents: %+v", b)
	}
	if tool, _ := b[0]["tool"].(map[string]any); tool["name"] != "scoped_tool" {
		t.Errorf("the bundled row is the wrong tool: %+v", b[0])
	}
	if o := got["orphaned"]; len(o) != 1 || o[0]["owner"] != "bob" || o[0]["former_agent_name"] != "Gone" {
		t.Errorf("the orphan should be listed with its owner: %+v", o)
	}

	// The access picker reads one tool's list and nothing else.
	SetPersistentTempToolAllowedUsers(f.a.db, "bob", "pool_tool", []string{"carol"})
	w = f.do(t, "GET", "/api/persistent-tools?allowed_users=pool_tool&owner=bob", "")
	if strings.TrimSpace(w.Body.String()) != `{"allowed_users":["carol"]}` {
		t.Errorf("the access picker got %s", w.Body.String())
	}
	w = f.do(t, "GET", "/api/persistent-tools?allowed_users=nothing&owner=bob", "")
	if strings.TrimSpace(w.Body.String()) != `{"allowed_users":[]}` {
		t.Errorf("an unknown tool's list should be empty, not null: %s", w.Body.String())
	}
}

func TestTheToolsPageChangesWhatItSays(t *testing.T) {
	f := newToolsFixture(t)
	f.seedBob(t)
	db := f.a.db
	ok := func(method, path, body string) {
		t.Helper()
		if w := f.do(t, method, path, body); w.Code != http.StatusNoContent {
			t.Fatalf("%s %s answered %d %s", method, path, w.Code, w.Body.String())
		}
	}
	pool := func(name string) (PersistentTempTool, bool) {
		for _, p := range LoadPersistentTempTools(db, "bob") {
			if p.Tool.Name == name {
				return p, true
			}
		}
		return PersistentTempTool{}, false
	}

	ok("POST", "/api/persistent-tools?action=approve&owner=bob&name=waiting_tool", "")
	if _, in := pool("waiting_tool"); !in || len(LoadPendingTempTools(db, "bob")) != 0 {
		t.Error("approving should move the tool from the queue to the pool")
	}
	ok("POST", "/api/persistent-tools?action=share&owner=bob&name=pool_tool", "")
	if p, _ := pool("pool_tool"); !p.Shared {
		t.Error("share did not share it")
	}
	ok("POST", "/api/persistent-tools?action=unshare&owner=bob&name=pool_tool", "")
	if p, _ := pool("pool_tool"); p.Shared {
		t.Error("unshare did not unshare it")
	}
	ok("POST", "/api/persistent-tools?action=set_allowed_users&owner=bob&name=pool_tool", `{"allowed_users":["dan"]}`)
	if p, _ := pool("pool_tool"); len(p.AllowedUsers) != 1 || p.AllowedUsers[0] != "dan" {
		t.Errorf("the access list was not set: %v", p.AllowedUsers)
	}
	ok("POST", "/api/persistent-tools?action=orphan_delete&owner=bob&name=lost_tool", "")
	if len(LoadOrphanedTempTools(db, "bob")) != 0 {
		t.Error("orphan_delete left the orphan")
	}
	ok("DELETE", "/api/persistent-tools?owner=bob&name=pool_tool", "")
	if _, in := pool("pool_tool"); in {
		t.Error("DELETE left the tool")
	}

	// Re-homing an orphan is orchestrate's to do; the page hands it over.
	prev := AdminRehomeOrphanTool
	t.Cleanup(func() { AdminRehomeOrphanTool = prev })
	var rehomed []string
	AdminRehomeOrphanTool = func(_ Database, owner, name, target string) error {
		rehomed = append(rehomed, owner+"/"+name+"->"+target)
		return nil
	}
	ok("POST", "/api/persistent-tools?action=orphan_promote&owner=bob&name=x", "")
	ok("POST", "/api/persistent-tools?action=orphan_attach&owner=bob&name=y&agent=a-1", "")
	if strings.Join(rehomed, " ") != "bob/x->global bob/y->a-1" {
		t.Errorf("re-homes handed over: %v", rehomed)
	}

	// The queue is rejected the same way it is approved.
	db.Set("pending_temp_tools", "bob", []PendingTempTool{{Tool: TempTool{Name: "nope_tool", Description: "d", CommandTemplate: "echo n"}}})
	ok("POST", "/api/persistent-tools?action=reject&owner=bob&name=nope_tool", "")
	if len(LoadPendingTempTools(db, "bob")) != 0 {
		t.Error("reject left the tool queued")
	}
}

func TestTheToolsPageRefusesWhatItCannotDo(t *testing.T) {
	f := newToolsFixture(t)
	prev := AdminRehomeOrphanTool
	AdminRehomeOrphanTool = nil
	t.Cleanup(func() { AdminRehomeOrphanTool = prev })
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"POST", "/api/persistent-tools?action=share", "", 400, "missing name"},
		{"DELETE", "/api/persistent-tools", "", 400, "missing name"},
		{"POST", "/api/persistent-tools?action=zz&name=x", "", 400, "action must be approve|reject"},
		{"POST", "/api/persistent-tools?action=set_allowed_users&name=x", "not json", 400, "invalid JSON"},
		{"POST", "/api/persistent-tools?action=set_allowed_users&name=x", "", 400, "bad request"},
		{"POST", "/api/persistent-tools?action=orphan_promote&name=x", "", 503, "orchestrate not wired"},
		{"POST", "/api/persistent-tools?action=orphan_attach&name=x", "", 400, "requires agent"},
		{"POST", "/api/persistent-tools?action=orphan_delete&name=x", "", 404, "no orphaned tool named x"},
		{"PUT", "/api/persistent-tools", "", 405, "method not allowed"},
		{"GET", "/api/tool-scope", "", 400, "missing name"},
		{"GET", "/api/tool-scope?name=x&kind=zz_unwired", "", 503, "scope kind zz_unwired not wired"},
	} {
		w := f.do(t, tc.method, tc.path, tc.body)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s answered %d %q, want %d with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.want)
		}
	}
}

// The scope pills read and toggle through whichever kind's provider is wired.
func TestTheScopePillsReachTheirKindsProvider(t *testing.T) {
	f := newToolsFixture(t)
	var set []string
	RegisterScopeProvider("zz_test_kind", ScopeProvider{
		State: func(_ Database, owner, name string) (ToolScopeState, bool) {
			return ToolScopeState{Name: owner + "/" + name, Global: true}, name != "missing"
		},
		Set: func(_ Database, owner, name, target string, on bool) error {
			if target == "bad" {
				return errors.New("no such agent")
			}
			set = append(set, owner+"/"+name+":"+target)
			return nil
		},
	})
	w := f.do(t, "GET", "/api/tool-scope?kind=zz_test_kind&owner=bob&name=x", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"bob/x"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("state: %d %s cache=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	if w := f.do(t, "GET", "/api/tool-scope?kind=zz_test_kind&name=missing", ""); w.Code != 404 {
		t.Errorf("a missing item answered %d", w.Code)
	}
	if w := f.do(t, "POST", "/api/tool-scope?kind=zz_test_kind&owner=bob&name=x", `{"target":"a-1","on":true}`); w.Code != 204 || strings.Join(set, " ") != "bob/x:a-1" {
		t.Errorf("toggle: %d, set %v", w.Code, set)
	}
	for body, want := range map[string]string{`{"target":" "}`: "target is required", `{"target":"bad"}`: "no such agent", `nope`: "invalid JSON"} {
		if w := f.do(t, "POST", "/api/tool-scope?kind=zz_test_kind&name=x", body); w.Code != 400 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("POST %s answered %d %q", body, w.Code, w.Body.String())
		}
	}
	if w := f.do(t, "DELETE", "/api/tool-scope?kind=zz_test_kind&name=x", ""); w.Code != 405 {
		t.Errorf("DELETE answered %d", w.Code)
	}
}
