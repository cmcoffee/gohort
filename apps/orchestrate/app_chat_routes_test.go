package orchestrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The endpoints an app's chat reaches orchestrate through (the agents app,
// customapps, filestore, scribe, anvil), and orchestrate's own inject, confirm
// and cancel, take the methods named here and no other, and refuse a caller who
// is not signed in. A malformed body is refused rather than read as nothing.
//
// Only refusals are sent, so nothing is renamed, cleared, revoked or stopped.
func TestTheAppChatEndpointsTakeTheirMethods(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice")
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	agent := AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada"}
	mux := routed(T)
	takes := []struct {
		name    string
		methods string
		h       http.HandlerFunc
	}{
		{"/api/inject", "GET POST PATCH DELETE", mux},
		{"/api/confirm", "POST", mux},
		{"/api/cancel", "POST", mux},
		{"inject", "GET POST PATCH DELETE", T.PublicHandleInject},
		{"confirm", "POST", T.PublicHandleConfirm},
		{"cancel", "POST", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleCancel(w, r, AppAgentOf(agent)) }},
		{"channel clear", "POST", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleChannelClear(w, r, agent.ID) }},
		{"agent", "GET", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleAgentRecord(w, r, agent) }},
		{"agent for scope", "GET", func(w http.ResponseWriter, r *http.Request) {
			T.PublicHandleAgentRecordForScope(w, r, "alice", agent.ID)
		}},
		{"sessions", "GET", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionList(w, r, agent.ID) }},
		{"sessions for", "GET", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionListFor(w, r, agent.ID, "doc") }},
		{"sessions only", "GET", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionListOnly(w, r, agent.ID, "doc") }},
		{"diagnostics", "GET", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionDiag(w, r, agent.ID, "s1") }},
		{"rename", "POST", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionRename(w, r, agent.ID) }},
		{"grants", "GET DELETE", func(w http.ResponseWriter, r *http.Request) { T.PublicHandleGrants(w, r, "proj", "g1") }},
	}
	for _, e := range takes {
		path := "/x"
		if strings.HasPrefix(e.name, "/") {
			path = e.name
		}
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			do := func(user, body string) int {
				r := httptest.NewRequest(m, path, strings.NewReader(body))
				if user != "" {
					r = withSession(r, user)
				}
				w := httptest.NewRecorder()
				e.h(w, r)
				return w.Code
			}
			if !strings.Contains(" "+e.methods+" ", " "+m+" ") {
				if code := do("alice", "{}"); code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, e.name, code)
				}
				continue
			}
			if code := do("", "{}"); code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, e.name, code)
			}
			if m != "GET" && e.name != "channel clear" && e.name != "grants" {
				if code := do("alice", "not json"); code != http.StatusBadRequest {
					t.Errorf("a malformed body to %s %s answered %d, want 400", m, e.name, code)
				}
			}
		}
	}
}

// What the app-chat endpoints reach is the caller's own: their running turn,
// their approval card, their sessions, their grants. Another signed-in user
// naming the same ids stops, answers, reads and revokes nothing.
func TestTheAppChatEndpointsReachOnlyTheCallersOwn(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob")
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "alice")
	if _, err := saveChatSession(udb, ChatSession{ID: "s1", AgentID: "agent-a", Title: "one",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	grant := saveToolGrant(udb, ToolGrant{Scope: "proj", Tool: "shell"})

	q := registerInjectionQueue("s1", "alice", "agent-a")
	t.Cleanup(func() { releaseInjectionQueue("s1") })
	noteID, _ := q.Push("first")

	confirm := &pendingToolConfirm{user: "alice", ch: make(chan bool, 1), answer: make(chan string, 1)}
	toolConfirms.Store("c1", confirm)
	t.Cleanup(func() { toolConfirms.Delete("c1") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inflightCancels.Store(runSessKey("alice", "s1"), context.CancelFunc(cancel))
	t.Cleanup(func() { inflightCancels.Delete(runSessKey("alice", "s1")) })

	do := func(h http.HandlerFunc, user, method, path, body string) *httptest.ResponseRecorder {
		r := withSession(httptest.NewRequest(method, path, strings.NewReader(body)), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	mux := routed(T)

	if w := do(mux, "bob", "POST", "/api/inject", `{"id":"s1","text":"mine now"}`); w.Code != http.StatusNotFound {
		t.Errorf("bob's note into alice's run answered %d", w.Code)
	}
	if w := do(mux, "bob", "DELETE", "/api/inject", `{"id":"s1","note_id":"`+noteID+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("bob's delete of alice's note answered %d", w.Code)
	}
	if n := len(q.Pending()); n != 1 {
		t.Errorf("alice's queue holds %d notes after bob's tries, want 1", n)
	}
	if w := do(mux, "bob", "POST", "/api/confirm", `{"id":"c1","value":"allow"}`); w.Code != http.StatusForbidden {
		t.Errorf("bob answering alice's card answered %d", w.Code)
	}
	if len(confirm.ch) != 0 {
		t.Error("bob's answer reached alice's approval")
	}
	do(mux, "bob", "POST", "/api/cancel?id=s1", "")
	do(T.PublicHandleInject, "bob", "POST", "/x", `{"id":"s1","text":"mine now"}`)
	pubCancel := func(w http.ResponseWriter, r *http.Request) {
		T.PublicHandleCancel(w, r, AppAgentOf(AgentRecord{ID: "agent-a"}))
	}
	do(pubCancel, "bob", "POST", "/x?id=s1", "")
	if ctx.Err() != nil {
		t.Error("bob stopped alice's turn")
	}
	if w := do(mux, "alice", "POST", "/api/cancel?id=s1&agent_id=nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("a cancel naming an agent alice cannot reach answered %d", w.Code)
	}
	if ctx.Err() != nil {
		t.Error("a cancel naming an agent alice cannot reach stopped her turn")
	}

	sessions := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionList(w, r, "agent-a") }
	if w := do(sessions, "bob", "GET", "/x", ""); strings.Contains(w.Body.String(), `"s1"`) {
		t.Errorf("bob listed alice's session: %s", w.Body.String())
	}
	rename := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleSessionRename(w, r, "agent-a") }
	do(rename, "bob", "POST", "/x", `{"id":"s1","name":"bob's now"}`)
	if s, _ := loadChatSession(udb, "agent-a", "s1"); s.Title != "one" {
		t.Errorf("bob renamed alice's session to %q", s.Title)
	}
	grants := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleGrants(w, r, "proj", grant.ID) }
	if w := do(grants, "bob", "GET", "/x", ""); strings.Contains(w.Body.String(), grant.ID) {
		t.Errorf("bob listed alice's grant: %s", w.Body.String())
	}
	if w := do(grants, "bob", "DELETE", "/x", ""); w.Code != http.StatusNotFound {
		t.Errorf("bob revoking alice's grant answered %d", w.Code)
	}
	if len(listToolGrants(udb, "proj")) != 1 {
		t.Error("bob revoked alice's grant")
	}
	// A grant outside the scope the app asked about is not revocable through it.
	other := func(w http.ResponseWriter, r *http.Request) { T.PublicHandleGrants(w, r, "other", grant.ID) }
	if w := do(other, "alice", "DELETE", "/x", ""); w.Code != http.StatusNotFound {
		t.Errorf("revoking a grant through another scope answered %d", w.Code)
	}

	// And the owner reaches all of it.
	if w := do(mux, "alice", "POST", "/api/confirm", `{"id":"c1","value":"allow"}`); w.Code != http.StatusNoContent || len(confirm.ch) != 1 {
		t.Errorf("alice could not answer her own card: %d", w.Code)
	}
	if w := do(grants, "alice", "DELETE", "/x", ""); w.Code != http.StatusOK || len(listToolGrants(udb, "proj")) != 0 {
		t.Errorf("alice could not revoke her own grant: %d", w.Code)
	}
	if w := do(mux, "alice", "POST", "/api/cancel?id=s1", ""); w.Code != http.StatusNoContent || ctx.Err() == nil {
		t.Errorf("alice could not stop her own turn: %d", w.Code)
	}
}

// An app agent's settings take their methods and no other: the data GET to
// read, PATCH or POST to save, and reset POST. A caller who is not signed in
// is refused, and a malformed save is refused rather than read as nothing.
func TestAppAgentSettingsTakeTheirMethods(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-methods", Name: "Methodical", OwningApp: "Zz Methods", Hidden: true, Prompt: "x",
	})
	T, udb, _ := newTestOrchestrate(t)
	agent, _ := loadAgent(udb, "app-test-methods")
	c := AppChat{Prefix: "chat/", Settings: true}
	serve := func(signed bool, method, path, body string) int {
		r := httptest.NewRequest(method, "/"+path, strings.NewReader(body))
		if signed {
			r = asUser(r, "alice")
		}
		w := httptest.NewRecorder()
		if !T.ServeAppChat(w, r, AppAgentOf(agent), c, path, "") {
			t.Fatalf("%s %s was not answered", method, path)
		}
		return w.Code
	}
	takes := map[string]string{"chat/settings/data": "GET PATCH POST", "chat/settings/reset": "POST"}
	for path, methods := range takes {
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if code := serve(true, m, path, "{}"); code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, code)
				}
				continue
			}
			if code := serve(false, m, path, "{}"); code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, code)
			}
		}
	}
	for _, m := range []string{"PATCH", "POST"} {
		if code := serve(true, m, "chat/settings/data", "not json"); code != http.StatusBadRequest {
			t.Errorf("a malformed %s save answered %d, want 400", m, code)
		}
	}
	if code := serve(false, "GET", "chat/settings", ""); code != http.StatusUnauthorized {
		t.Errorf("signed out, the settings page answered %d, want 401", code)
	}
}
