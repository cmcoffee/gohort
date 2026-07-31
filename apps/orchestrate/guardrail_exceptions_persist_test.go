package orchestrate

// End-to-end persistence for the guardrails endpoint.
//
// Exceptions "would not save" twice in a row while every unit along the path
// passed: the endpoint decoded them, the handler assigned them, and the record
// round-tripped through storage. What none of those covered is that loadAgent
// does not return what saveAgent stored for every KIND of agent — a seed is
// rebuilt from code with only named fields overlaid from its shadow. A field
// missing from that overlay saves perfectly and reads back empty forever.
//
// So these go through the HTTP handler and back out through loadAgent, once per
// agent kind, which is the only shape of test that could have caught it.

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

func guardrailsPersistApp(t *testing.T) (*OrchestrateApp, Database) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	return app, UserDB(root, "u")
}

// authAs wires AuthDB to a scratch store and returns a cookie that
// authenticates as "u", so a test can drive the handlers that resolve the user
// themselves (handleAgentList) rather than being handed one.
func authAs(t *testing.T) *http.Cookie {
	t.Helper()
	saved := AuthDB
	authStore := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return authStore }
	t.Cleanup(func() { AuthDB = saved })
	token := AuthCreateSession(authStore, "u")
	if token == "" {
		t.Fatal("could not mint a session")
	}
	return &http.Cookie{Name: "gohort_session", Value: token}
}

// postGuardrails drives the real endpoint with a body carrying one exception
// and one roster entry, and fails on any non-2xx.
func postGuardrails(t *testing.T, app *OrchestrateApp, agentID string) {
	t.Helper()
	body := `{"guardrails":"@confirmed never send money","hooks":["pre_output"],` +
		`"authorized":["dana"],` +
		`"exceptions":[{"name":"confirmed","text":"the user has already confirmed"}]}`
	r := httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/guardrails", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	app.handleAgentGuardrails(w, r, "u", agentID)
	if w.Code >= 300 {
		t.Fatalf("POST guardrails returned %d: %s", w.Code, w.Body.String())
	}
}

// TestExceptionsPersistOnOrdinaryAgent — the baseline. A plain agent record is
// stored and read back whole.
func TestExceptionsPersistOnOrdinaryAgent(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	postGuardrails(t, app, "a1")

	got, ok := loadAgent(udb, "a1")
	if !ok {
		t.Fatal("agent vanished")
	}
	if len(got.GuardrailExceptions) != 1 || got.GuardrailExceptions[0].Name != "confirmed" {
		t.Errorf("exceptions did not survive: %+v", got.GuardrailExceptions)
	}
	if len(got.AuthorizedIdentities) != 1 {
		t.Errorf("roster did not survive: %+v", got.AuthorizedIdentities)
	}
	if got.Guardrails == "" {
		t.Error("rules did not survive")
	}
}

// TestExceptionsPersistOnSeedAgent — a seed keeps its shadow as the BASE, so
// everything the owner configured has to come back with it.
func TestExceptionsPersistOnSeedAgent(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	seeds := seedAgents()
	var seedID string
	for _, s := range seeds {
		if s.ID != "seed-builder" {
			seedID = s.ID
			break
		}
	}
	if seedID == "" {
		t.Skip("no non-Builder seed to exercise")
	}
	postGuardrails(t, app, seedID)

	got, ok := loadAgent(udb, seedID)
	if !ok {
		t.Fatalf("seed %s vanished", seedID)
	}
	if len(got.GuardrailExceptions) != 1 {
		t.Errorf("exceptions dropped on seed %s: %+v", seedID, got.GuardrailExceptions)
	}
	if len(got.AuthorizedIdentities) != 1 {
		t.Errorf("roster dropped on seed %s: %+v", seedID, got.AuthorizedIdentities)
	}
}

// TestModalReadsBackWhatItSaved — the full loop the owner performs: save from
// the Rules modal, reload the page, reopen. The modal repopulates from GET
// /api/agents/{id}, NOT from the guardrails endpoint, so a field that persists
// perfectly and is simply absent from that payload looks exactly like a save
// that did nothing.
func TestModalReadsBackWhatItSaved(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	postGuardrails(t, app, "a1")

	// What the browser gets when the modal reopens. handleAgentOne's GET is
	// loadAgent followed by json.Encode of the record, so the record's own
	// marshaling IS the payload — reproduced here because the handler itself
	// requires an authenticated session this test has no way to mint.
	got, ok := loadAgent(udb, "a1")
	if !ok {
		t.Fatal("agent vanished")
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal agent: %v", err)
	}
	payload := string(blob)
	// Asserted on the WIRE keys, because those are what the modal reads. A
	// renamed or omitempty-stripped tag is invisible to a Go-level round trip
	// and fatal to the UI.
	for _, key := range []string{`"guardrail_exceptions"`, `"authorized_identities"`, `"confirmed"`, `"dana"`} {
		if !strings.Contains(payload, key) {
			t.Errorf("the modal's reload payload is missing %s:\n%s", key, payload)
		}
	}
}

// TestModalSaveSequence — the Rules modal performs TWO writes per save: the
// whole record to /api/agents, then the guardrails endpoint. Every other test
// here exercises the second one alone, which is why they all passed while the
// owner watched exceptions vanish. This replays both, in the modal's order,
// with the record payload a browser actually sends — the agent as it was
// FETCHED, i.e. carrying whatever guardrail fields it was loaded with.
func TestModalSaveSequence(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	cookie := authAs(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Write 1: the whole record, exactly as the modal sends it — the fetched
	// agent with the non-guardrail edits applied. It carries NO exceptions,
	// because the modal keeps those in its own state, not on `agent`.
	record := `{"id":"a1","name":"X","owner":"u","orchestrator_prompt":"p","rules":"be brief"}`
	r := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(record))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.handleAgentList(w, r)
	if w.Code >= 300 {
		t.Fatalf("whole-record POST returned %d: %s", w.Code, w.Body.String())
	}

	// Write 2: the guardrails endpoint, carrying the exceptions.
	postGuardrails(t, app, "a1")

	got, ok := loadAgent(udb, "a1")
	if !ok {
		t.Fatal("agent vanished")
	}
	if len(got.GuardrailExceptions) != 1 {
		t.Errorf("exceptions lost across the modal's two-write save: %+v", got.GuardrailExceptions)
	}
	if len(got.AuthorizedIdentities) != 1 {
		t.Errorf("roster lost across the modal's two-write save: %+v", got.AuthorizedIdentities)
	}
	if got.Rules != "be brief" {
		t.Errorf("the record edit was lost: rules=%q", got.Rules)
	}

	// And the SECOND save of the same session — reopen, change nothing about
	// exceptions, save again. The modal re-posts the record it fetched, which
	// now DOES carry the stored exceptions.
	record2 := `{"id":"a1","name":"X","owner":"u","orchestrator_prompt":"p","rules":"be brief",` +
		`"guardrail_exceptions":[{"name":"confirmed","text":"the user has already confirmed"}],` +
		`"authorized_identities":["dana"]}`
	r = httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(record2))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	app.handleAgentList(w, r)
	if w.Code >= 300 {
		t.Fatalf("second whole-record POST returned %d: %s", w.Code, w.Body.String())
	}
	got, _ = loadAgent(udb, "a1")
	if len(got.GuardrailExceptions) != 1 {
		t.Errorf("a plain record re-save dropped the exceptions: %+v", got.GuardrailExceptions)
	}
}

// TestStalePageDoesNotWipeExceptions — a client that does not know about these
// fields must leave them alone, not clear them. A browser holding a cached copy
// of the page from before the field existed posts a body with no "exceptions"
// key at all, and the destroying version of this was indistinguishable from the
// feature simply not saving.
func TestStalePageDoesNotWipeExceptions(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "X", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	postGuardrails(t, app, "a1")

	// A save from a page that predates exceptions/authorized entirely.
	old := `{"guardrails":"never send money","hooks":["pre_output"],"declines":[]}`
	r := httptest.NewRequest(http.MethodPost, "/api/agents/a1/guardrails", bytes.NewBufferString(old))
	w := httptest.NewRecorder()
	app.handleAgentGuardrails(w, r, "u", "a1")
	if w.Code >= 300 {
		t.Fatalf("stale POST returned %d: %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, "a1")
	if len(got.GuardrailExceptions) != 1 {
		t.Errorf("a page that never mentioned exceptions wiped them: %+v", got.GuardrailExceptions)
	}
	if len(got.AuthorizedIdentities) != 1 {
		t.Errorf("a page that never mentioned the roster wiped it: %+v", got.AuthorizedIdentities)
	}

	// An EXPLICIT empty list still clears — that is the owner deleting them.
	clear := `{"guardrails":"never send money","hooks":["pre_output"],"exceptions":[],"authorized":[]}`
	r = httptest.NewRequest(http.MethodPost, "/api/agents/a1/guardrails", bytes.NewBufferString(clear))
	w = httptest.NewRecorder()
	app.handleAgentGuardrails(w, r, "u", "a1")
	got, _ = loadAgent(udb, "a1")
	if len(got.GuardrailExceptions) != 0 || len(got.AuthorizedIdentities) != 0 {
		t.Errorf("an explicit empty list must clear: %+v / %+v", got.GuardrailExceptions, got.AuthorizedIdentities)
	}
}

// TestExceptionsPersistOnBuilder — Builder is rebuilt from the in-code seed on
// every read, with a NAMED list of fields overlaid from its shadow. Anything
// absent from that list saves and then reads back empty, silently and forever.
func TestExceptionsPersistOnBuilder(t *testing.T) {
	app, udb := guardrailsPersistApp(t)
	postGuardrails(t, app, "seed-builder")

	got, ok := loadAgent(udb, "seed-builder")
	if !ok {
		t.Fatal("Builder vanished")
	}
	if got.Guardrails == "" {
		t.Error("guardrail rules dropped on Builder")
	}
	if len(got.GuardrailExceptions) != 1 {
		t.Errorf("exceptions dropped on Builder: %+v", got.GuardrailExceptions)
	}
	if len(got.AuthorizedIdentities) != 1 {
		t.Errorf("roster dropped on Builder: %+v", got.AuthorizedIdentities)
	}
	if len(got.GuardrailHooks) != 1 {
		t.Errorf("hooks dropped on Builder: %+v", got.GuardrailHooks)
	}
}
