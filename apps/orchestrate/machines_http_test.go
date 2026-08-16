package orchestrate

// The machines HTTP surface (machines_http.go) and the one place an
// agent gets pointed at a machine. Both are the whole path a person uses
// to try a machine live before the St3 UI exists, so they are worth a
// round trip through the real handlers rather than the store directly.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// authedApp returns an app whose requests authenticate as one user,
// plus a request builder that carries the session cookie.
func authedApp(t *testing.T) (*OrchestrateApp, func(method, path string, body any) *http.Request, Database) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	token := AuthCreateSession(adb, "alice")

	app := &OrchestrateApp{}
	app.DB = root
	req := func(method, path string, body any) *http.Request {
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		}
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		return r
	}
	return app, req, UserDB(root, "alice")
}

func machineBody() map[string]any {
	return map[string]any{
		"name":  "Triage",
		"start": "decompose",
		"phases": []map[string]any{
			{
				"name": "decompose", "desc": "Split the question up.",
				"prompt": "Break down: {input}", "next": "answer",
				"output": []map[string]any{{"name": "parts", "type": "list"}},
			},
			{
				"name": "answer", "desc": "Reply directly.", "resident": true,
				"prompt": "Answer, working from {state:decompose.parts}.",
			},
		},
	}
}

func TestMachinesHTTP_CreateListReadUpdateDelete(t *testing.T) {
	app, req, udb := authedApp(t)

	// Create.
	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodPost, "/api/machines", machineBody()))
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created MachineDef
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if created.ID == "" || created.Owner != "alice" || len(created.Phases) != 2 {
		t.Fatalf("create returned %+v", created)
	}

	// List.
	w = httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodGet, "/api/machines", nil))
	var list struct{ Machines []machineRow }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(list.Machines) != 1 || list.Machines[0].Phases != 2 || list.Machines[0].Start != "decompose" {
		t.Fatalf("list returned %+v", list.Machines)
	}

	// Read one.
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet, "/api/machines/"+created.ID, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "{state:decompose.parts}") {
		t.Fatalf("read one: %d %s", w.Code, w.Body.String())
	}

	// Update keeps identity, takes content.
	upd := machineBody()
	upd["name"] = "Triage v2"
	upd["id"] = "someone-elses-id"
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodPut, "/api/machines/"+created.ID, upd))
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var updated MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.ID != created.ID {
		t.Errorf("the client must not be able to reassign the id, got %s", updated.ID)
	}
	if updated.Name != "Triage v2" {
		t.Errorf("update should take the body's content, got %q", updated.Name)
	}

	// Export strips storage metadata.
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet, "/api/machines/"+created.ID+"/export", nil))
	var recipe MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &recipe)
	if recipe.Owner != "" || !recipe.Created.IsZero() {
		t.Errorf("export should strip owner/timestamps, got %+v", recipe)
	}

	// Delete.
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodDelete, "/api/machines/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, ok := LoadMachineDef(udb, "alice", created.ID); ok {
		t.Error("machine should be gone")
	}
}

func TestMachinesHTTP_RejectsAnInvalidMachineWithTheRealReason(t *testing.T) {
	app, req, _ := authedApp(t)
	bad := machineBody()
	// Every phase transient: nowhere for a user turn to land.
	bad["phases"].([]map[string]any)[1]["resident"] = false
	bad["phases"].([]map[string]any)[1]["next"] = "decompose"

	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodPost, "/api/machines", bad))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", w.Code, w.Body.String())
	}
	// The validator's message is the useful part — it has to reach the
	// author, not be swallowed into a generic "bad request".
	if !strings.Contains(w.Body.String(), "resident") {
		t.Errorf("expected the validator's reason, got %s", w.Body.String())
	}
}

func TestMachinesHTTP_ImportRoundTrip(t *testing.T) {
	app, req, _ := authedApp(t)
	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodPost, "/api/machines", machineBody()))
	var created MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet, "/api/machines/"+created.ID+"/export", nil))
	var recipe MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &recipe)

	// Importing the same recipe makes a COPY rather than clobbering the
	// original, because the traveled id is already taken.
	w = httptest.NewRecorder()
	app.handleMachineImport(w, req(http.MethodPost, "/api/machines/import", recipe))
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	var imported MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &imported)
	if imported.ID == created.ID {
		t.Error("re-importing over an existing id should remint, not clobber")
	}
	if imported.Owner != "alice" || len(imported.Phases) != 2 {
		t.Errorf("import landed wrong: %+v", imported)
	}
}

// The machine printed in docs/agent-machines.md under "Trying one live",
// verbatim. It is the first thing anyone will paste at this endpoint, and
// a documented example that 400s is worse than no example — so it is
// posted here rather than trusted.
const docExampleMachine = `{
    "name": "Triage",
    "start": "decompose",
    "phases": [
      {"name": "decompose", "desc": "Split the question up.",
       "prompt": "Break the user request below into its parts.\n\n{input}",
       "next": "route",
       "output": [{"name": "parts", "type": "list", "desc": "the distinct questions being asked"}]},

      {"name": "route", "desc": "Pick a lane.",
       "prompt": "Given these parts:\n{state:decompose.parts}\n\nPick the phase that should answer.",
       "next_from": "target", "next": "answer",
       "output": [{"name": "target", "type": "string", "desc": "either answer or deep"}]},

      {"name": "answer", "desc": "Reply directly.", "resident": true,
       "prompt": "Answer plainly, working from what is settled below.",
       "guard": "the user has moved on to a subject the earlier breakdown does not cover",
       "guard_to": "decompose"},

      {"name": "deep", "desc": "Long-form work.", "resident": true,
       "prompt": "Take your time. Show your reasoning and cite what you used.",
       "guard": "the user has moved on to a subject the earlier breakdown does not cover",
       "guard_to": "decompose"}
    ]
  }`

func TestMachinesHTTP_TheDocumentedExampleValidates(t *testing.T) {
	app, req, _ := authedApp(t)
	var body any
	if err := json.Unmarshal([]byte(docExampleMachine), &body); err != nil {
		t.Fatalf("the documented example is not valid JSON: %v", err)
	}
	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodPost, "/api/machines", body))
	if w.Code != http.StatusOK {
		t.Fatalf("the documented example was rejected: %d %s", w.Code, w.Body.String())
	}
	var saved MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &saved)
	if len(saved.Phases) != 4 || saved.StartPhase() != "decompose" {
		t.Errorf("saved wrong: %+v", saved)
	}
}

func TestAgentSave_WholeRecordFormDoesNotClearTheMachine(t *testing.T) {
	// The footgun this guards: there is no machine control on the agent
	// edit form until St3, so a normal save posts the whole record
	// WITHOUT it. Without preservation, attaching a machine and then
	// editing anything else in the UI silently detaches it. Same rule
	// the guardrail and lock fields already follow.
	app, req, udb := authedApp(t)

	w := httptest.NewRecorder()
	app.handleAgentList(w, req(http.MethodPost, "/api/agents", map[string]any{
		"name": "Wren", "orchestrator_prompt": "be helpful",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
	}
	var agent AgentRecord
	_ = json.Unmarshal(w.Body.Bytes(), &agent)

	// Attach via the partial-update path, which is how you do it today.
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/"+agent.ID, map[string]any{"machine": "m-123"}))
	if w.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, agent.ID); got.Machine != "m-123" {
		t.Fatalf("attach did not stick, got %q", got.Machine)
	}

	// Now a whole-record save from the edit form, carrying no machine.
	w = httptest.NewRecorder()
	app.handleAgentList(w, req(http.MethodPost, "/api/agents", map[string]any{
		"id": agent.ID, "name": "Wren", "orchestrator_prompt": "be helpful and brief",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("form save: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, agent.ID)
	if got.Machine != "m-123" {
		t.Errorf("a form with no machine control must not clear it, got %q", got.Machine)
	}
	if got.OrchestratorPrompt != "be helpful and brief" {
		t.Errorf("the save should still take the fields the form DOES show, got %q", got.OrchestratorPrompt)
	}
}

// The JSON editor in the Machines modal PUTs the recipe and shows
// whatever comes back. That makes the PUT path's error text a user
// interface, not a log line.
func TestMachinesHTTP_EditKeepsIdentityAndExplainsRejections(t *testing.T) {
	app, req, udb := authedApp(t)
	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodPost, "/api/machines", machineBody()))
	var created MachineDef
	_ = json.Unmarshal(w.Body.Bytes(), &created)

	// An agent pointing at it must stay pointed at it across an edit —
	// the whole reason update exists rather than create-a-new-one.
	ag, err := saveAgent(udb, AgentRecord{Name: "Wren", Owner: "alice", OrchestratorPrompt: "hi", Machine: created.ID})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// What the editor actually sends: the recipe, no storage metadata.
	edited := map[string]any{
		"name": "Triage", "description": "now with a guard", "start": "decompose",
		"phases": []map[string]any{
			{"name": "decompose", "prompt": "Break down: {input}", "next": "answer",
				"output": []map[string]any{{"name": "parts", "type": "list"}}},
			{"name": "answer", "resident": true, "prompt": "Answer.",
				"guard": "they changed the subject", "guard_to": "decompose"},
		},
	}
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodPut, "/api/machines/"+created.ID, edited))
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	back, ok := LoadMachineDef(udb, "alice", created.ID)
	if !ok || back.Description != "now with a guard" {
		t.Fatalf("the edit did not land: %+v", back)
	}
	if got, _ := loadAgent(udb, ag.ID); got.Machine != created.ID {
		t.Errorf("an edit must not detach the agents running it, got %q", got.Machine)
	}

	// A rejected edit has to say what is wrong — several things at once,
	// because the editor shows the response verbatim and a person fixing
	// one problem per save is a person who stops using the editor.
	broken := map[string]any{
		"name": "Triage", "start": "ghost",
		"phases": []map[string]any{
			{"name": "decompose", "prompt": "x", "next": "nowhere"},
			{"name": "answer", "resident": true, "prompt": "y", "model": "turbo"},
		},
	}
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodPut, "/api/machines/"+created.ID, broken))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	msg := w.Body.String()
	for _, want := range []string{"3 problems", "start names unknown step", "next names unknown step", "model must be"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the rejection should name %q; got: %s", want, msg)
		}
	}
	// And it must not have half-written the broken version.
	if again, _ := LoadMachineDef(udb, "alice", created.ID); again.Start != "decompose" {
		t.Errorf("a rejected edit must leave the stored machine alone, got start=%q", again.Start)
	}
}
