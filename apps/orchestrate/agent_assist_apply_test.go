package orchestrate

// The agent assistant's Apply button.
//
// It shipped 2026-09-09 sending PATCH to /api/agents/<id>. That route had
// answered GET, POST and DELETE since three days earlier and has never answered
// PATCH, so every press returned "method not allowed" for nine days. Nothing
// caught it: the panel builds, the route is registered, the proposal arrives,
// and the failure is one verb deep on a path nothing exercises until a person
// clicks.
//
// So the pieces are pinned end to end: the verb the client sends, the route
// that serves it, the id it takes from the path, and the allowlist every
// spelling has to go through.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// applyBlock returns the Apply handler's client source.
func applyBlock(t *testing.T) string {
	t.Helper()
	src := readFile(t, "page_agent.go")
	i := strings.Index(src, "apply.onclick=function(){")
	if i < 0 {
		t.Fatal("the Apply button is gone")
	}
	end := strings.Index(src[i:], "body.appendChild(el('div',{class:'aa-chat'}")
	if end < 0 {
		t.Fatal("could not bound the Apply handler")
	}
	return src[i : i+end]
}

func TestApplyTargetsARouteThatServesItsVerb(t *testing.T) {
	block := applyBlock(t)
	if !strings.Contains(block, "method:'PATCH'") {
		t.Fatal("Apply no longer PATCHes; this test is reading the wrong place")
	}
	// Whichever spelling it uses, the route behind it has to accept PATCH.
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	var path string
	switch {
	case strings.Contains(block, "'../api/agents/'+id"):
		path = "/api/agents/a1"
	case strings.Contains(block, "'../api/agents?id='"):
		path = "/api/agents?id=a1"
	default:
		t.Fatalf("Apply posts somewhere this test does not recognise; check the route serves PATCH:\n%s", block)
	}
	if w := patchAs(app, user, path, `{"description":"applied"}`); w.Code != http.StatusOK {
		t.Errorf("Apply PATCHes %s, which answers %d %s", path, w.Code, w.Body.String())
	}
}

// patchAs sends one write to the app's routes as user.
func patchAs(app *OrchestrateApp, user, path, body string) *httptest.ResponseRecorder {
	return writeAs(app, user, http.MethodPatch, path, body)
}

func writeAs(app *OrchestrateApp, user, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	routed(app)(w, asUser(httptest.NewRequest(method, path, strings.NewReader(body)), user))
	return w
}

// Every spelling must reach the SAME merge, or a second way to do something is
// a second set of rules for it. patchAgentFields leaves guardrails and the
// injection-scan settings out BY NAME so no partial save can weaken the rule it
// is about to be judged against; a path that skipped it would undo that.
func TestEverySpellingOfPatchGoesThroughTheAllowlist(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: user, Name: "Ada", OrchestratorPrompt: "p", Guardrails: "never curse"}); err != nil {
		t.Fatal(err)
	}
	for _, sp := range []struct{ method, path string }{
		{http.MethodPatch, "/api/agents/a1"},
		{http.MethodPost, "/api/agents/a1"},
		{http.MethodPatch, "/api/agents?id=a1"},
	} {
		if w := writeAs(app, user, sp.method, sp.path, `{"guardrails":"anything goes"}`); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s changed a protected field: %d %s", sp.method, sp.path, w.Code, w.Body.String())
		}
		if w := writeAs(app, user, sp.method, sp.path, `{"description":"`+sp.method+`"}`); w.Code != http.StatusOK {
			t.Errorf("%s %s refused an allowed field: %d %s", sp.method, sp.path, w.Code, w.Body.String())
		}
	}
	if got, _ := loadAgent(udb, "a1"); got.Guardrails != "never curse" {
		t.Errorf("the guardrails changed: %q", got.Guardrails)
	}
	// The fields the assistant may propose all have to be settable, or Apply
	// succeeds on some changes and 400s on others with no warning beforehand.
	assist := readFile(t, "agent_assist.go")
	for field := range assistableFields {
		if !patchAgentFields[field] {
			t.Errorf("assist may propose %q but PATCH refuses it: Apply fails on any proposal touching it", field)
		}
		if !strings.Contains(assist, `"`+field+`"`) {
			t.Errorf("%q is not named in agent_assist.go", field)
		}
	}
}

// The target is the path's id, else the query's, else the body's; with none
// of them the PATCH is refused rather than guessed.
func TestThePathIdReachesTheMerge(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	for _, id := range []string{"a1", "a2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: user, Name: id, OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ path, body, id string }{
		{"/api/agents/a1", `{"description":"by path"}`, "a1"},
		{"/api/agents?id=a2", `{"description":"by query"}`, "a2"},
		{"/api/agents", `{"id":"a1","description":"by body"}`, "a1"},
	} {
		if w := patchAs(app, user, c.path, c.body); w.Code != http.StatusOK {
			t.Fatalf("PATCH %s: %d %s", c.path, w.Code, w.Body.String())
		}
		got, _ := loadAgent(udb, c.id)
		if !strings.Contains(c.body, got.Description) {
			t.Errorf("PATCH %s landed elsewhere: %s has %q", c.path, c.id, got.Description)
		}
	}
	if w := patchAs(app, user, "/api/agents", `{"description":"nowhere"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a PATCH naming no agent: %d %s", w.Code, w.Body.String())
	}
}

// funcBody returns one function's source, from its signature to the closing
// brace at column 0.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("no %q", sig)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("%q never closes", sig)
	}
	return src[i : i+j]
}

// A POST to one agent is a partial save, and the protected fields stay out of
// it: it once merged the whole body onto the record, so one POST could clear
// an agent's guardrails, switch off its scanner, or re-parent it past the move
// endpoint's check, which every other way to save an agent refuses.
func TestAPostToOneAgentCannotWeakenItsGuardrails(t *testing.T) {
	app, req, udb := authedApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p",
		Guardrails: "Never delete anything.", ScanToolResults: true}); err != nil {
		t.Fatal(err)
	}
	post := func(body map[string]any) int {
		w := httptest.NewRecorder()
		routed(app)(w, req("POST", "/api/agents/agent-1", body))
		return w.Code
	}
	for _, body := range []map[string]any{
		{"guardrails": ""},
		{"guardrails_disabled": true},
		{"scan_tool_results": false},
		{"owned_by": "agent-2"},
	} {
		if code := post(body); code < 400 {
			t.Errorf("POST %v answered %d, want it refused", body, code)
		}
	}
	got, _ := loadAgent(udb, "agent-1")
	if got.Guardrails != "Never delete anything." || got.GuardrailsDisabled || !got.ScanToolResults || got.OwnedBy != "" {
		t.Errorf("a refused POST changed the agent: %+v", got)
	}
	// The fields a partial save may change still change.
	if code := post(map[string]any{"name": "Renamed", "allowed_dispatch_targets": []string{"agent-3"}}); code != 200 {
		t.Fatalf("an allowed field answered %d", code)
	}
	if got, _ := loadAgent(udb, "agent-1"); got.Name != "Renamed" || len(got.AllowedDispatchTargets) != 1 {
		t.Errorf("an allowed POST did not save: %+v", got)
	}
}
