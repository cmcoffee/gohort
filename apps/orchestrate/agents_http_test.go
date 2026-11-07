package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The Limits form loads the action limits as rows and saves them through the
// agent's PATCH: a row read back is the same row, the record keeps its
// "action = N" lines for every other reader, a half-filled row is refused
// with what is missing, and an empty list clears.
func TestTheLimitsListRoundTrips(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Ada", OrchestratorPrompt: "p",
		ActionQuotas: ActionQuotaMap{"send_email": 3}, DailySpendUSD: 2}); err != nil {
		t.Fatal(err)
	}
	load := func() map[string]any {
		w := writeAs(app, user, http.MethodGet, "/api/agents/a1/limits", "")
		if w.Code != http.StatusOK {
			t.Fatalf("load: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	got, _ := json.Marshal(load())
	if string(got) != `{"action_quotas":[{"action":"send_email","limit":3}],"daily_spend_usd":2}` {
		t.Errorf("the form's load: %s", got)
	}

	body := `{"action_quotas":[{"action":"moltbook/create_post","limit":6},{"action":"send_email","limit":"2"},{"action":"","limit":0}],"daily_spend_usd":2}`
	if w := patchAs(app, user, "/api/agents/a1", body); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	a, _ := loadAgent(udb, "a1")
	if len(a.ActionQuotas) != 2 || a.ActionQuotas["moltbook/create_post"] != 6 || a.ActionQuotas["send_email"] != 2 {
		t.Errorf("saved %v", a.ActionQuotas)
	}
	if raw, _ := json.Marshal(a.ActionQuotas); string(raw) != `["moltbook/create_post = 6","send_email = 2"]` {
		t.Errorf("the record's shape for every other reader changed: %s", raw)
	}
	if rows := load()["action_quotas"].([]any); len(rows) != 2 {
		t.Errorf("read back %v", rows)
	}

	for _, bad := range []struct{ body, want string }{
		{`{"action_quotas":[{"action":"send_email","limit":0}]}`, "how many times"},
		{`{"action_quotas":[{"action":"send_email","limit":1.5}]}`, "how many times"},
		{`{"action_quotas":[{"action":"","limit":4}]}`, "name the action"},
	} {
		if w := patchAs(app, user, "/api/agents/a1", bad.body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), bad.want) || strings.Contains(w.Body.String(), "bad field value") {
			t.Errorf("%s: %d %s", bad.body, w.Code, w.Body.String())
		}
	}
	if a, _ := loadAgent(udb, "a1"); len(a.ActionQuotas) != 2 {
		t.Errorf("a refused save changed the limits: %v", a.ActionQuotas)
	}
	if w := patchAs(app, user, "/api/agents/a1", `{"action_quotas":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if a, _ := loadAgent(udb, "a1"); len(a.ActionQuotas) != 0 {
		t.Errorf("an empty list should clear the limits: %v", a.ActionQuotas)
	}
	// Somebody else's agent is not found. (asUser always sends the first
	// user's session, so the second user signs in for real.)
	AuthDB().Set(AuthTable, "user:user2", AuthUser{Username: "user2"})
	r := httptest.NewRequest(http.MethodGet, "/api/agents/a1/limits", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "user2")})
	w := httptest.NewRecorder()
	routed(app)(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("another user read the limits: %d %s", w.Code, w.Body.String())
	}
}
