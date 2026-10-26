package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every route registerAgentRoutes mounts takes the methods named here and no
// other, and refuses a caller who is not signed in. Another user naming the
// owner's agent by id gets a 404 from every read of it.
//
// Only refusals and reads are asked for, so nothing here saves, clones,
// deletes or calls a model.
func TestEveryAgentRouteTakesItsMethodsAndRefusesTheSignedOut(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob")
	if _, err := saveAgent(UserDB(root, "alice"), AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(user, method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	takes := map[string]string{
		"":                 "GET POST PATCH DELETE",
		"clone":            "POST",
		"detach":           "POST",
		"move":             "GET POST",
		"schedules":        "GET",
		"used-by":          "GET",
		"reach":            "GET",
		"reach/credential": "POST",
		"missing":          "GET POST",
		"guardrails":       "GET POST",
		"guardrail-depth":  "GET POST",
		"guardrail-test":   "POST",
		"decline-suggest":  "POST",
	}
	src := funcBody(t, readFile(t, "agents_http.go"), "func (T *OrchestrateApp) registerAgentRoutes")
	if n := strings.Count(src, `T.HandleFunc("/api/agents/{id}`); n != len(takes)+5 {
		t.Fatalf("registerAgentRoutes mounts %d routes under /api/agents/{id}; this table names %d and the five raw ones: add the new one here", n, len(takes))
	}
	for action, methods := range takes {
		path := "/api/agents/agent-a"
		if action != "" {
			path += "/" + action
		}
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if w := do("alice", m, path); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, w.Code)
				}
				continue
			}
			if w := do("", m, path); w.Code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, w.Code)
			}
		}
	}
	// The raw routes resolve the caller themselves.
	for _, action := range []string{"export", "knowledge/upload", "assist", "revisions", "revisions/preview"} {
		if w := do("", "GET", "/api/agents/agent-a/"+action); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", action, w.Code)
		}
	}
	// Reads of the agent find only the caller's own.
	for _, action := range []string{"", "guardrails", "guardrail-depth", "missing", "reach", "export"} {
		path := "/api/agents/agent-a"
		if action != "" {
			path += "/" + action
		}
		if w := do("bob", "GET", path); w.Code != http.StatusNotFound {
			t.Errorf("bob, GET %s answered %d, want 404", path, w.Code)
		}
		if w := do("alice", "GET", path); w.Code != http.StatusOK {
			t.Errorf("alice, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	// The collection.
	if w := do("", "GET", "/api/agents"); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out, GET /api/agents answered %d", w.Code)
	}
	for _, m := range []string{"PUT", "DELETE"} {
		if w := do("alice", m, "/api/agents"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/agents answered %d, want 405", m, w.Code)
		}
	}
	if w := do("bob", "GET", "/api/agents"); strings.Contains(w.Body.String(), "agent-a") {
		t.Errorf("bob's list shows alice's agent: %s", w.Body.String())
	}
}
