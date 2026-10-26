package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every Memory pane route takes the methods named here and no other, and
// refuses a caller who is not signed in. Another user asking for the owner's
// agent by id gets a 404 from the routes that read the agent record; the
// rest read the caller's own store, so they only ever show the caller theirs.
//
// Only refusals are asked for, and reads, so nothing here wipes or deletes.
func TestEveryMemoryRouteTakesItsMethodsAndRefusesTheSignedOut(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })
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
		"facts":                         "GET POST",
		"notes":                         "GET POST",
		"memaudit":                      "GET POST",
		"memsearch":                     "GET DELETE",
		"inferred":                      "GET",
		"inferred/x":                    "DELETE",
		"graph":                         "GET",
		"graph/edge":                    "DELETE",
		"graph/entity/x":                "DELETE",
		"graph/entity/x/attr":           "DELETE",
		"graph/entity/x/alias":          "DELETE",
		"knowledge":                     "GET DELETE",
		"knowledge/auto-inferred":       "GET DELETE",
		"knowledge/sources":             "GET",
		"knowledge/sources/x":           "DELETE",
		"knowledge/scaffold-collection": "POST",
		"phantom-sessions":              "GET DELETE",
		"phantom-sessions/x":            "GET DELETE",
	}
	src, err := os.ReadFile("memory_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), `T.HandleFunc("/api/agents/{id}/`); n != len(takes) {
		t.Fatalf("memory_routes.go registers %d routes and this table names %d: add the new one here", n, len(takes))
	}
	for action, methods := range takes {
		path := "/api/agents/agent-a/" + action
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
	// The routes that read the agent record find only the caller's own, or
	// one shared with them.
	for _, action := range []string{"facts", "notes", "memaudit", "memsearch?q=x"} {
		path := "/api/agents/agent-a/" + action
		if w := do("bob", "GET", path); w.Code != http.StatusNotFound {
			t.Errorf("bob, GET %s answered %d, want 404", path, w.Code)
		}
		if w := do("alice", "GET", path); w.Code != http.StatusOK {
			t.Errorf("alice, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
}

// The other apps' doors serve the same routes: the agents app for the
// signed-in visitor, servitor for a scope it has already authorized. A scope
// reads the scope's store, whoever is signed in.
func TestTheOtherAppsDoorsReadTheStoreTheyName(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })
	signedUp(t, "alice", "bob")
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	if _, err := saveAgent(UserDB(root, "alice"), AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	StoreMemoryFact(UserDB(root, "alice"), factsNamespace("agent-a"), "likes tea")
	get := func(user string, door func(w http.ResponseWriter, r *http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/x", nil)
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		door(w, r)
		return w
	}
	scope := func(w http.ResponseWriter, r *http.Request) {
		T.PublicHandleAgentFactsForScope(w, r, "alice", "agent-a")
	}
	if w := get("bob", scope); w.Code != 200 || !strings.Contains(w.Body.String(), "likes tea") {
		t.Errorf("the scope's facts: %d %s", w.Code, w.Body.String())
	}
	if w := get("", scope); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out, the scope door answered %d", w.Code)
	}
	visitor := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleAgentFacts(w, r, "agent-a") }
	if w := get("alice", visitor); w.Code != 200 || !strings.Contains(w.Body.String(), "likes tea") {
		t.Errorf("the owner's facts: %d %s", w.Code, w.Body.String())
	}
	if w := get("bob", visitor); w.Code != http.StatusNotFound {
		t.Errorf("bob read alice's agent through the visitor door: %d %s", w.Code, w.Body.String())
	}
}
