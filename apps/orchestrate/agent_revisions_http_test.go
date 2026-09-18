package orchestrate

// The revision surface end to end: list, read one without applying it, and go
// back. Worth a round trip through the real handlers rather than the store,
// because the payload IS the contract the shared History button renders.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
)

type histPayload struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Empty    string `json:"empty"`
	Entries  []struct {
		Title   string `json:"title"`
		Detail  string `json:"detail"`
		Actions []struct {
			Label   string `json:"label"`
			URL     string `json:"url"`
			Method  string `json:"method"`
			Kind    string `json:"kind"`
			Confirm string `json:"confirm"`
			Variant string `json:"variant"`
		} `json:"actions"`
	} `json:"entries"`
}

// seedEditedAgent creates an agent and edits it twice, so there is history.
func seedEditedAgent(t *testing.T, udb Database) AgentRecord {
	t.Helper()
	a, err := saveAgent(udb, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1", Owner: "alice"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, p := range []string{"v2", "v3"} {
		a.OrchestratorPrompt = p
		if _, err := saveAgentAs(udb, a, "edited instructions"); err != nil {
			t.Fatalf("edit: %v", err)
		}
	}
	return a
}

func TestAgentRevisionsListIsWhatTheHistoryButtonRenders(t *testing.T) {
	app, req, udb := authedApp(t)
	a := seedEditedAgent(t, udb)

	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("GET", "/api/agents/"+a.ID+"/revisions", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got histPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	if len(got.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(got.Entries))
	}
	// Newest first, and the reason travels with the entry so the list reads as
	// a history rather than a column of ids.
	if !strings.HasPrefix(got.Entries[0].Title, "#2") {
		t.Errorf("first entry = %q, want the newest", got.Entries[0].Title)
	}
	if got.Entries[0].Detail != "edited instructions" {
		t.Errorf("detail = %q", got.Entries[0].Detail)
	}
	if got.Empty == "" || got.Subtitle == "" {
		t.Error("the payload must say what an empty list means and how far back the ring goes")
	}
	acts := got.Entries[0].Actions
	if len(acts) != 2 {
		t.Fatalf("got %d actions, want preview + restore", len(acts))
	}
	if acts[0].Kind != "show" {
		t.Errorf("preview kind = %q, want show", acts[0].Kind)
	}
	// Relative to the history url, never absolute: the editor reaches the api
	// through its own base and an absolute path here would have to guess it.
	for _, a := range acts {
		if strings.HasPrefix(a.URL, "/") || strings.HasPrefix(a.URL, "api/") {
			t.Errorf("action url %q must be a sibling of the history url", a.URL)
		}
		if !strings.HasPrefix(a.URL, "revisions/") {
			t.Errorf("action url %q", a.URL)
		}
	}
	if acts[1].Method != "post" || acts[1].Confirm == "" {
		t.Errorf("restore action = %+v — a restore confirms", acts[1])
	}
}

// An agent with no edits yet says so rather than rendering an empty box.
func TestAgentRevisionsListIsEmptyBeforeAnyEdit(t *testing.T) {
	app, req, udb := authedApp(t)
	a, err := saveAgent(udb, AgentRecord{Name: "Scout", OrchestratorPrompt: "v1", Owner: "alice"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("GET", "/api/agents/"+a.ID+"/revisions", nil))
	var got histPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %+v, want none", got.Entries)
	}
}

// The preview answers "what did this used to say" without applying anything,
// which is the question history gets opened for.
func TestAgentRevisionPreviewNamesWhatMovedAndChangesNothing(t *testing.T) {
	app, req, udb := authedApp(t)
	a := seedEditedAgent(t, udb)

	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("GET", "/api/agents/"+a.ID+"/revisions/preview?rev=1", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var view struct{ Title, Text string }
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(view.Title, "#1") {
		t.Errorf("title = %q", view.Title)
	}
	if !strings.Contains(view.Text, "orchestrator_prompt") {
		t.Errorf("the changed field must be named: %q", view.Text)
	}
	// The kept side, printed as itself — the field people open this for is a
	// prompt, and JSON-quoting prose makes it unreadable.
	if !strings.Contains(view.Text, "v1") {
		t.Errorf("the kept value must be shown: %q", view.Text)
	}
	if cur, _ := loadAgent(udb, a.ID); cur.OrchestratorPrompt != "v3" {
		t.Errorf("a preview changed the agent: %q", cur.OrchestratorPrompt)
	}
}

func TestAgentRevisionRestoreGoesBackAndStaysReversible(t *testing.T) {
	app, req, udb := authedApp(t)
	a := seedEditedAgent(t, udb)

	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("POST", "/api/agents/"+a.ID+"/revisions/restore?rev=1", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	cur, ok := loadAgent(udb, a.ID)
	if !ok || cur.OrchestratorPrompt != "v1" {
		t.Fatalf("restored prompt = %q, want v1", cur.OrchestratorPrompt)
	}
	revs := revisions.List(udb, revisions.KindAgent, a.ID)
	if len(revs) != 3 {
		t.Fatalf("got %d revisions after restore, want 3 — the restore files what it replaced", len(revs))
	}
	if !strings.HasPrefix(revs[0].Reason, "rolled back to #1") {
		t.Errorf("newest reason = %q", revs[0].Reason)
	}
}

// A lock exists so nothing changes this agent until the owner takes it off,
// and a restore is the largest edit there is.
func TestAgentRevisionRestoreRefusesALockedAgent(t *testing.T) {
	app, req, udb := authedApp(t)
	a := seedEditedAgent(t, udb)
	if _, err := setAgentLocked(udb, a, true); err != nil {
		t.Fatalf("lock: %v", err)
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("POST", "/api/agents/"+a.ID+"/revisions/restore?rev=1", nil))
	if w.Code != 403 {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
	}
	if cur, _ := loadAgent(udb, a.ID); cur.OrchestratorPrompt != "v3" {
		t.Errorf("a locked agent was restored anyway: %q", cur.OrchestratorPrompt)
	}
}

func TestAgentRevisionRestoreRefusesAnUnknownVersion(t *testing.T) {
	app, req, udb := authedApp(t)
	a := seedEditedAgent(t, udb)
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("POST", "/api/agents/"+a.ID+"/revisions/restore?rev=99", nil))
	if w.Code == 200 {
		t.Error("an id that was never issued must not restore")
	}
}

// Another user's agent is not theirs to read the history of, let alone restore.
func TestAgentRevisionsRefuseSomebodyElsesAgent(t *testing.T) {
	app, req, _ := authedApp(t)
	other := UserDB(app.DB, "bob")
	a, err := saveAgent(other, AgentRecord{Name: "Bob's", OrchestratorPrompt: "v1", Owner: "bob"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	edited := a
	edited.OrchestratorPrompt = "v2"
	if _, err := saveAgent(other, edited); err != nil {
		t.Fatalf("edit: %v", err)
	}
	for _, path := range []string{
		"/api/agents/" + a.ID + "/revisions",
		"/api/agents/" + a.ID + "/revisions/preview?rev=1",
	} {
		w := httptest.NewRecorder()
		app.handleAgentOne(w, req("GET", path, nil))
		if w.Code == 200 {
			t.Errorf("%s served another user's history", path)
		}
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req("POST", "/api/agents/"+a.ID+"/revisions/restore?rev=1", nil))
	if w.Code == 200 {
		t.Error("another user's agent was restored")
	}
	if cur, _ := loadAgent(other, a.ID); cur.OrchestratorPrompt != "v2" {
		t.Errorf("another user's agent changed: %q", cur.OrchestratorPrompt)
	}
}

// The same three routes, for the other two kinds this app owns. One surface
// serves all of them, so these check the wiring rather than re-testing the
// payload: the right store, the right kind, and the owner gate.
func TestMachineRevisionRoutes(t *testing.T) {
	app, req, udb := authedApp(t)
	made := SaveMachineDef(udb, MachineDef{Name: "Triage", Start: "decompose", Owner: "alice"})
	edited := made
	edited.Name = "Triage v2"
	SaveMachineDefAs(udb, edited, "renamed")

	w := httptest.NewRecorder()
	app.handleMachineOne(w, req("GET", "/api/machines/"+made.ID+"/revisions", nil))
	if w.Code != 200 {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	var got histPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	if len(got.Entries) != 1 || got.Entries[0].Detail != "renamed" {
		t.Fatalf("entries = %+v", got.Entries)
	}
	if !strings.Contains(got.Empty, "machine") {
		t.Errorf("the empty state must name the thing: %q", got.Empty)
	}

	w = httptest.NewRecorder()
	app.handleMachineOne(w, req("POST", "/api/machines/"+made.ID+"/revisions/restore?rev=1", nil))
	if w.Code != 200 {
		t.Fatalf("restore status %d: %s", w.Code, w.Body.String())
	}
	if back, _ := LoadMachineDef(udb, "alice", made.ID); back.Name != "Triage" {
		t.Errorf("restored name = %q, want Triage", back.Name)
	}
}

func TestPipelineRevisionRoutes(t *testing.T) {
	app, req, udb := authedApp(t)
	made := SavePipelineDef(udb, PipelineDef{Name: "Digest", Owner: "alice"})
	edited := made
	edited.Name = "Digest v2"
	SavePipelineDefAs(udb, edited, "renamed")

	w := httptest.NewRecorder()
	app.handlePipelineOne(w, req("GET", "/api/pipelines/"+made.ID+"/revisions", nil))
	if w.Code != 200 {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	var got histPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %+v", got.Entries)
	}

	w = httptest.NewRecorder()
	app.handlePipelineOne(w, req("GET", "/api/pipelines/"+made.ID+"/revisions/preview?rev=1", nil))
	if w.Code != 200 {
		t.Fatalf("preview status %d: %s", w.Code, w.Body.String())
	}
	var view struct{ Title, Text string }
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(view.Text, "name") || !strings.Contains(view.Text, "Digest") {
		t.Errorf("preview must name what moved and show the kept value: %q", view.Text)
	}
}
