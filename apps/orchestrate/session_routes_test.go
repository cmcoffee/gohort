package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every session route takes the methods named here and no other, refuses a
// caller who is not signed in, needs ?agent_id=, and reaches only sessions of
// the caller's own agents: another user naming the owner's agent finds
// nothing.
//
// Only reads and refusals are sent, so nothing is deleted, renamed or handed
// to Builder.
func TestEverySessionRouteTakesItsMethodsAndItsOwner(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob")
	udb := UserDB(root, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveChatSession(udb, ChatSession{ID: "s1", AgentID: "agent-a", Title: "one",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	takes := map[string]string{
		"mark-all-read":        "POST",
		"rename":               "POST",
		"s1":                   "GET DELETE PATCH",
		"s1/continue":          "POST",
		"s1/send-to-builder":   "POST",
		"s1/blocks/b1/resolve": "POST",
		"s1/tools":             "GET",
		"s1/tools/x":           "POST",
	}
	for p, methods := range takes {
		path := "/api/sessions/" + p + "?agent_id=agent-a"
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if w := do("alice", m, path, "{}"); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, w.Code)
				}
				continue
			}
			if w := do("", m, path, "{}"); w.Code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, w.Code)
			}
			// bob names alice's agent and finds nothing.
			if w := do("bob", m, path, "{}"); w.Code != http.StatusNotFound {
				t.Errorf("bob, %s %s answered %d, want 404", m, path, w.Code)
			}
			// No agent named, nothing resolved.
			if w := do("alice", m, "/api/sessions/"+p, "{}"); w.Code != http.StatusBadRequest {
				t.Errorf("no agent_id, %s %s answered %d, want 400", m, p, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/sessions/s1/export?agent_id=agent-a"} {
		if w := do("", "GET", path, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", path, w.Code)
		}
		if w := do("bob", "GET", path, ""); w.Code != http.StatusNotFound {
			t.Errorf("bob, GET %s answered %d, want 404", path, w.Code)
		}
	}
	if w := do("alice", "GET", "/api/sessions/s1?agent_id=agent-a", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Title":"one"`) {
		t.Errorf("alice could not open her own session: %d %s", w.Code, w.Body.String())
	}
	if _, ok := loadChatSession(udb, "agent-a", "s1"); !ok {
		t.Error("the session is gone")
	}
}

// The other apps' session door (the agents app, customapps, filestore) is the
// same route as orchestrate's, for the agent they let the caller reach: the
// thread comes back trimmed with its offset, and clearing it drops the rolling
// summary too, so the next turn is not re-primed with the old conversation.
func TestTheOtherAppsSessionDoorIsOrchestratesRoute(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "bob")
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "bob")
	if _, err := saveChatSession(udb, ChatSession{ID: "s1", AgentID: "agent-x", Title: "visit",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	saveCompactState(udb, "agent-x", "s1", CompactState{Summary: "they said hi", SummarizedThrough: 1})
	door := func(user, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/agents/x/api/sessions/s1", strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		T.PublicHandleSessionOne(w, r, "agent-x", "s1")
		return w
	}
	if w := door("", "GET", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", w.Code)
	}
	if w := door("bob", "PUT", "{}"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", w.Code)
	}
	if w := door("bob", "GET", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"message_offset":0`) || !strings.Contains(w.Body.String(), `"chunk_size"`) {
		t.Errorf("the load is not the served session: %d %s", w.Code, w.Body.String())
	}
	if w := door("bob", "PATCH", `{"at":0}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if st := loadCompactState(udb, "agent-x", "s1"); st.Summary != "" {
		t.Errorf("clearing the thread kept its rolling summary: %q", st.Summary)
	}
	if w := door("bob", "DELETE", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if _, ok := loadChatSession(udb, "agent-x", "s1"); ok {
		t.Error("the session survived its delete")
	}
}
