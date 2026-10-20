package orchestrate

// An agent's share list: people, a team, or Everyone. Everyone is publishing,
// filed for an administrator unless one is asking, and the names beside it are
// kept through publishing and taking it back.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEveryoneInAnAgentsShareListIsARequest(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	prevAdmin := requestIsAdminAgent
	admin := false
	requestIsAdminAgent = func(*http.Request) bool { return admin }
	t.Cleanup(func() { requestIsAdminAgent = prevAdmin })
	if _, err := saveAgent(udb, AgentRecord{ID: "a51", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	share := func(method, body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		app.handleAgentShare(w, asUser(httptest.NewRequest(method, "/api/console/permissions/share?agent=a51", strings.NewReader(body)), "alice"))
		if w.Code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", method, w.Code, w.Body.String())
		}
		var got struct {
			AllowedUsers []string `json:"allowed_users"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		return strings.Join(got.AllowedUsers, ",")
	}
	rec := func() AgentRecord { a, _ := loadAgent(udb, "a51"); return a }
	pending := func() bool { return publishPending(agentPromotionKind, "alice", agentPromotionName("a51", "exposed")) }

	if got := share(http.MethodPost, `{"allowed_users":["*","bob"]}`); got != "*,bob" || rec().Everyone || !pending() {
		t.Errorf("an owner's Everyone: list %q, published %v, requested %v", got, rec().Everyone, pending())
	}
	if got := share(http.MethodGet, ""); got != "*,bob" {
		t.Errorf("while it waits the list reads %q", got)
	}
	if got := share(http.MethodPost, `{"allowed_users":["bob"]}`); got != "bob" || pending() {
		t.Errorf("taking Everyone out: list %q, still requested %v", got, pending())
	}

	admin = true
	if got := share(http.MethodPost, `{"allowed_users":["*","bob"]}`); got != "*,bob" || !rec().Everyone {
		t.Errorf("an administrator's Everyone: list %q, published %v", got, rec().Everyone)
	}
	admin = false
	if got := share(http.MethodPost, `{"allowed_users":["bob","carol"]}`); got != "bob,carol" || rec().Everyone {
		t.Errorf("taken back: list %q, published %v", got, rec().Everyone)
	}
}
