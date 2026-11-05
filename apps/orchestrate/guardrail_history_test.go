package orchestrate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/gohort/core/revisions"
)

// Changing guardrails files the guardrails it replaced in their own history,
// not the agent's: a few minutes of tuning them pushed the persona and rules
// an owner wanted back out of the agent's six kept versions.
func TestGuardrailChangesKeepAHistoryOfTheirOwn(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "the original persona"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgentAs(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "a later persona"}, "edited"); err != nil {
		t.Fatal(err)
	}
	agentKept := len(revisions.List(udb, revisions.KindAgent, "a1"))
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		routed(app)(w, as(t, r, "u"))
		return w
	}
	for _, rules := range []string{"never share the address", "never share the phone number", "never share either"} {
		if w := post("/api/agents/a1/guardrails", `{"guardrails":"`+rules+`","hooks":["pre_output"]}`); w.Code >= 300 {
			t.Fatalf("guardrails: %d %s", w.Code, w.Body.String())
		}
	}
	for i := 0; i < 8; i++ {
		post("/api/agents/a1/guardrails", `{"guardrails":"rule `+string(rune('a'+i))+`"}`)
	}
	if got := len(revisions.List(udb, revisions.KindAgent, "a1")); got != agentKept {
		t.Errorf("guardrail saves filed agent versions: %d kept, was %d", got, agentKept)
	}
	if !revisions.Load(udb, revisions.KindAgent, "a1", "", &AgentRecord{}) {
		t.Fatal("the agent's own history is gone")
	}
	var oldest AgentRecord
	revs := revisions.List(udb, revisions.KindAgent, "a1")
	revisions.Load(udb, revisions.KindAgent, "a1", "#"+itoa(revs[len(revs)-1].Seq), &oldest)
	if oldest.OrchestratorPrompt != "the original persona" {
		t.Errorf("the original persona was pushed out: %q", oldest.OrchestratorPrompt)
	}
	if got := len(revisions.List(udb, revisions.KindAgentGuardrails, "a1")); got != revisions.Kept {
		t.Errorf("guardrail history: %d kept, want %d", got, revisions.Kept)
	}

	// Back to an earlier set of guardrails, through the history surface.
	g := revisions.List(udb, revisions.KindAgentGuardrails, "a1")
	var want guardrailSet
	revisions.Load(udb, revisions.KindAgentGuardrails, "a1", "#"+itoa(g[0].Seq), &want)
	if w := post("/api/agents/a1/guardrails/revisions/restore?rev="+itoa(g[0].Seq), ""); w.Code >= 300 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	a, _ := loadAgent(udb, "a1")
	if a.Guardrails != want.Guardrails || a.OrchestratorPrompt != "a later persona" {
		t.Errorf("after restoring guardrails: guardrails %q (want %q), persona %q", a.Guardrails, want.Guardrails, a.OrchestratorPrompt)
	}

	// Back to an earlier persona keeps today's guardrails.
	now := a.Guardrails
	if _, err := rollbackAgent(udb, "a1", ""); err != nil {
		t.Fatal(err)
	}
	a, _ = loadAgent(udb, "a1")
	if a.OrchestratorPrompt != "the original persona" || a.Guardrails != now {
		t.Errorf("after an agent rollback: persona %q, guardrails %q (want them kept as %q)", a.OrchestratorPrompt, a.Guardrails, now)
	}
}

// Only the owner reads or restores an agent's guardrail history.
func TestGuardrailHistoryIsTheOwners(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/agents/a1/guardrails/revisions", nil)
	w := httptest.NewRecorder()
	routed(app)(w, as(t, r, "someone-else"))
	if w.Code != http.StatusNotFound {
		t.Errorf("another user read the guardrail history: %d", w.Code)
	}
}

