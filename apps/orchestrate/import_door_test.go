package orchestrate

// The per-type Import buttons take a form upload, a bare recipe, or a bundle;
// the unified importer takes the per-type buttons' bare files.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func importDoorFixture(t *testing.T) (*OrchestrateApp, Database, string) {
	t.Helper()
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	RegisterAgentArtifactType(T)
	RegisterPipelineArtifactType(T)
	RegisterMachineArtifactType(T)
	return T, udb, user
}

func postImport(t *testing.T, h http.HandlerFunc, path, user string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, asUser(httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)), user))
	return w
}

func pipelineRecipe(t *testing.T, name string) []byte {
	t.Helper()
	d := StarterPipeline()
	d.Name = name
	raw, err := json.Marshal(ExportPipeline(d))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The page's Import form posts {"recipe": "<file text>"}. The door decoded that
// as a PipelineDef, so every import from the UI read as an empty pipeline.
func TestThePipelineImportFormWorks(t *testing.T) {
	T, udb, user := importDoorFixture(t)
	form, _ := json.Marshal(map[string]string{"recipe": string(pipelineRecipe(t, "From the form"))})
	w := postImport(t, T.handlePipelineImport, "/api/pipelines/import", user, form)
	if w.Code != http.StatusOK {
		t.Fatalf("form import: %d %s", w.Code, w.Body.String())
	}
	if _, ok := findPipelineByNameOrID(udb, user, "From the form"); !ok {
		t.Fatal("the pipeline was not saved")
	}
	// The bare recipe a script posts still works.
	if w := postImport(t, T.handlePipelineImport, "/api/pipelines/import", user, pipelineRecipe(t, "Bare")); w.Code != http.StatusOK {
		t.Fatalf("bare import: %d %s", w.Code, w.Body.String())
	}
}

func TestAPipelineDoorTakesABundleAndReportsTheRest(t *testing.T) {
	T, udb, user := importDoorFixture(t)
	bundle, _ := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{
		{Type: "pipeline", Name: "Bundled", Recipe: pipelineRecipe(t, "Bundled")},
		{Type: "credential", Name: "crm", Recipe: json.RawMessage(`{"name": "crm", "type": "header"}`)},
	}})
	form, _ := json.Marshal(map[string]string{"recipe": string(bundle)})
	w := postImport(t, T.handlePipelineImport, "/api/pipelines/import", user, form)
	if w.Code != http.StatusOK {
		t.Fatalf("bundle import: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	saved, ok := findPipelineByNameOrID(udb, user, "Bundled")
	if !ok || out["id"] != saved.ID {
		t.Fatalf("the door should answer with the imported pipeline (id for the redirect): %v", out)
	}
	msg, _ := out["message"].(string)
	if !strings.Contains(msg, "Imported 1, skipped 1") {
		t.Errorf("the rest of the bundle should be reported: %q", msg)
	}
}

// A bundle with none of the door's type imports NOTHING, rather than landing a
// stranger's other artifacts from a button that promised a pipeline.
func TestADoorRefusesABundleWithoutItsType(t *testing.T) {
	T, udb, user := importDoorFixture(t)
	agent, _ := json.Marshal(agentExport{AgentRecord: AgentRecord{Name: "Stray", OrchestratorPrompt: "p"}})
	bundle, _ := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{
		{Type: "agent", Name: "Stray", Recipe: agent},
	}})
	w := postImport(t, T.handlePipelineImport, "/api/pipelines/import", user, bundle)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "holds no pipeline") {
		t.Fatalf("want a refusal naming what the bundle holds: %d %s", w.Code, w.Body.String())
	}
	for _, a := range listAgents(udb, user) {
		if a.Name == "Stray" {
			t.Fatal("the refused bundle imported its agent anyway")
		}
	}
}

// The agent button's own file, handed to the unified importer, reads as an
// agent. It used to fall through to the connector parser and fail as
// "unknown connector kind".
func TestAnAgentFileReadsAsAnAgentInTheBundleImporter(t *testing.T) {
	T, udb, user := importDoorFixture(t)
	_ = T
	legacy, _ := json.Marshal(agentExport{AgentRecord: AgentRecord{Name: "Legacy", OrchestratorPrompt: "p"}})
	res, err := ImportArtifactBundleAsUser(RootDB, legacy, user)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Imported != 1 || res.Outcomes[0].Type != "agent" {
		t.Fatalf("expected one agent, got %+v", res.Outcomes)
	}
	found := false
	for _, a := range listAgents(udb, user) {
		found = found || a.Name == "Legacy"
	}
	if !found {
		t.Error("the agent was not saved")
	}
	// And the agent button still takes its own file, making a copy each time.
	for i := 0; i < 2; i++ {
		if w := postImport(t, T.handleAgentImport, "/api/agents/import", user, legacy); w.Code != http.StatusOK {
			t.Fatalf("agent door: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestAnOversizedImportIsRefusedPlainly(t *testing.T) {
	T, _, user := importDoorFixture(t)
	big := bytes.Repeat([]byte(" "), maxImportBytes+1)
	if w := postImport(t, T.handleMachineImport, "/api/machines/import", user, big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", w.Code)
	}
}
