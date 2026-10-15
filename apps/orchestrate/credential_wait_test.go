package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Drafting a credential ends Builder's turn: the secret comes from the person,
// after this turn, so nothing built on it can work yet. With nothing said
// alongside the draft, the reply says what to enter and what happens next,
// rather than the turn ending in "no reply".
func TestACredentialDraftEndsTheTurn(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	AuthDB().Set(AuthTable, "user:"+user, AuthUser{Username: user, Admin: true})
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "draft_api_credential", Args: map[string]any{"name": "acme", "type": "bearer", "base_url": "https://api.acme.test"}}}},
		{Content: "Now testing the tool against acme."},
	}}
	T.LLM = fake
	agent, ok := loadAgent(udb, "seed-builder")
	if !ok {
		t.Fatal("no Builder")
	}
	body, _ := json.Marshal(map[string]any{"message": "Make me a tool for the Acme API."})
	w := httptest.NewRecorder()
	T.handleSend(w, asUser(httptest.NewRequest(http.MethodPost, "/api/send?agent_id=seed-builder", strings.NewReader(string(body))), user), udb, user, agent)
	out := w.Body.String()
	if strings.Contains(out, "Now testing the tool") {
		t.Error("the turn went on after the draft")
	}
	if !strings.Contains(out, "credential_setup") || !strings.Contains(out, "The acme credential needs the bearer token") {
		t.Errorf("no setup card or no reply saying what to enter:\n%s", out)
	}
	if strings.Contains(out, `"kind":"error"`) {
		t.Errorf("the turn ended in an error:\n%s", out)
	}
}
