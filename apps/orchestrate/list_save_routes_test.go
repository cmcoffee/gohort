package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The routes that read with GET and save with POST (or PATCH, or DELETE) take
// those methods and no other, refuse a caller who is not signed in, and refuse
// a save with no body rather than read it as an empty one: an empty save of
// the default agent would clear it, of a share list would unshare it.
func TestTheListAndSaveRoutesTakeTheirMethods(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice")
	udb := UserDB(root, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(root, StandingAgent{Owner: "alice", Name: "nightly", AgentID: "agent-a"})
	AuthSetDefaultAgent(AuthDB(), "alice", "agent-a")
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(user, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	takes := map[string]string{
		"/api/default-agent":                                    "GET POST",
		"/api/tool-confirm?agent=agent-a":                       "GET POST",
		"/api/tool-scope?name=web_search":                       "GET POST",
		"/api/console/scheduler/notes?kind=standing&id=nightly": "GET POST",
		"/api/console/permissions/share?agent=agent-a":          "GET POST PATCH",
		"/api/agent-credentials?id=agent-a":                     "GET POST",
		"/api/channels?agent_id=agent-a":                        "GET POST DELETE",
		"/api/bulletins":                                        "GET POST",
		"/api/reference-sources?agent=agent-a":                  "GET POST",
		"/api/collections":                                      "GET POST",
		"/api/pipelines":                                        "GET POST",
		"/api/machines":                                         "GET POST",
	}
	for path, methods := range takes {
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if code := do("alice", m, path, "{}"); code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, code)
				}
				continue
			}
			if code := do("", m, path, "{}"); code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, code)
			}
			if m == "POST" || m == "PATCH" {
				if code := do("alice", m, path, ""); code != http.StatusBadRequest {
					t.Errorf("an empty %s %s answered %d, want 400", m, path, code)
				}
			}
		}
	}
	if got := AuthGetDefaultAgent(AuthDB(), "alice"); got != "agent-a" {
		t.Errorf("an empty save changed the default agent to %q", got)
	}
	if a, _ := loadAgent(udb, "agent-a"); len(a.AllowedUsers) != 1 {
		t.Errorf("an empty save changed the share list to %v", a.AllowedUsers)
	}
}

// The share list and the credential picker reach only an agent the caller may
// change: a sub-agent is its parent's and is not shared on its own, and a
// record in the caller's store that names another owner is not theirs to
// scope.
func TestShareAndCredentialsReachOnlyTheCallersOwnAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice")
	udb := UserDB(root, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "sub-a", Owner: "alice", OwnedBy: "agent-a", Name: "Helper", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	foreign := AgentRecord{ID: "theirs", Owner: "bob", Name: "Theirs", OrchestratorPrompt: "p"}
	udb.Set(agentsTable, foreign.ID, foreign)
	if a, ok := loadAgent(udb, "theirs"); !ok || a.Owner != "bob" {
		t.Fatalf("fixture: the foreign record reads as %+v %v", a, ok)
	}
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(method, path, body string) int {
		w := httptest.NewRecorder()
		h(w, withSession(httptest.NewRequest(method, path, strings.NewReader(body)), "alice"))
		return w.Code
	}
	for _, m := range []string{"GET", "POST"} {
		if code := do(m, "/api/console/permissions/share?agent=sub-a", `{"allowed_users":["bob"]}`); code != http.StatusNotFound {
			t.Errorf("%s on a sub-agent's share list answered %d, want 404", m, code)
		}
		if code := do(m, "/api/agent-credentials?id=theirs", `{"enabled_credentials":[]}`); code != http.StatusNotFound {
			t.Errorf("%s on another owner's agent's credentials answered %d, want 404", m, code)
		}
	}
	if a, _ := loadAgent(udb, "sub-a"); len(a.AllowedUsers) != 0 {
		t.Errorf("the sub-agent was shared: %v", a.AllowedUsers)
	}
	if code := do("GET", "/api/console/permissions/share?agent=agent-a", ""); code != http.StatusOK {
		t.Errorf("alice could not read her own agent's share list: %d", code)
	}
}
