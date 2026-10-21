package orchestrate

// The agent editor's assists run on core/assist: what reached the person
// before reaches them now, through the one handler. A conversation turn
// comes back as the reply and the cleaned draft; a request with no message
// is still the one-shot suggestion; the whole-agent assist still proposes
// only the fields it may change; the routes are registered private worker
// stages.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func assistAppFixture(t *testing.T, fake *FakeLLM) (*OrchestrateApp, func(path, body string) *http.Request) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:u", AuthUser{Username: "u"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	req := func(path, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
		return r
	}
	return T, req
}

func TestTheFieldSuggestRunsOnTheAssistHandler(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{
		{Content: "Made it shorter.\n```draft\n\"You are Scout.\"\n```"},
		{Content: "Scout\nRanger\nSeeker"},
	}}
	T, req := assistAppFixture(t, fake)
	call := func(body string) (*httptest.ResponseRecorder, map[string]any) {
		w := httptest.NewRecorder()
		T.handleAgentSuggest(w, req("/api/agents/suggest", body))
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	// A turn of the workbench: the reply, and the draft cleaned for its field.
	w, out := call(`{"field":"orchestrator_prompt","message":"shorter","draft":"You are Scout, a helper.","mode":"write"}`)
	if w.Code != 200 || out["reply"] != "Made it shorter." || out["value"] != "You are Scout." {
		t.Fatalf("a workbench turn: %d %s", w.Code, w.Body.String())
	}
	cfg := fake.Config(0)
	if cfg.RouteKey != "app.orchestrate.suggest" || cfg.Think == nil || *cfg.Think || !strings.Contains(cfg.SystemPrompt, "ONE value in an AI agent's definition") {
		t.Errorf("the turn should carry its own prompt, route and no thinking: %q %v", cfg.RouteKey, cfg.Think)
	}
	// No message: the one-shot, a name with a few to choose from.
	w, out = call(`{"field":"name","record":{"description":"looks things up"}}`)
	if vals, _ := out["values"].([]any); w.Code != 200 || len(vals) < 2 {
		t.Errorf("the one-shot name should offer several: %d %s", w.Code, w.Body.String())
	}
	if w, _ := call(`{"field":"allowed_tools","message":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a field that is not suggestable: %d", w.Code)
	}
	if !IsPrivateStage("app.orchestrate.suggest") {
		t.Error("the suggest route should be a private worker stage")
	}
}

func TestTheWholeAgentAssistRunsOnTheAssistHandler(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"reply":"Added a rule.","changes":[{"field":"rules","why":"stops guessing","value":"Cite every claim. Say when you do not know."},{"field":"allowed_tools","value":"browse_page"}]}`}}}
	T, req := assistAppFixture(t, fake)
	udb := &DBase{Store: kvlite.MemStore()}
	rec := assistTestAgent()
	rec.Owner = "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	T.handleAgentAssist(w, req("/api/agents/a1/assist", `{"message":"it guesses"}`), "u", udb, rec.ID)
	var out assistReply
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Reply != "Added a rule." || len(out.Changes) != 1 || out.Changes[0].Field != "rules" {
		t.Fatalf("want the reply and the one change it may make: %d %s", w.Code, w.Body.String())
	}
	if fake.Config(0).RouteKey != "app.orchestrate.assist" || !IsPrivateStage("app.orchestrate.assist") {
		t.Error("the whole-agent assist's route should be registered, a private worker stage")
	}
	w = httptest.NewRecorder()
	T.handleAgentAssist(w, req("/api/agents/a1/assist", `{"message":"x"}`), "someone-else", udb, rec.ID)
	if w.Code != http.StatusForbidden {
		t.Errorf("another person's agent: %d", w.Code)
	}
}
