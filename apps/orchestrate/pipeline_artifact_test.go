package orchestrate

// Tests for the "pipeline" artifact type: recipe shape (ID travels,
// owner/timestamps don't, agent refs normalize to names), import collision
// rules, and the two-way closure with agents — a pipeline folds in the agents
// its stages dispatch to, an agent folds in its AttachedPipelines, and the
// import preview matches the ID-based reference against the traveling recipe.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// pipelineArtifactApp builds an app on a fresh mem store and registers the
// agent + pipeline artifact types against it. The registry is global and last
// registration wins, so each test binds the types to its own store.
func pipelineArtifactApp(t *testing.T) *OrchestrateApp {
	t.Helper()
	app := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	RegisterAgentArtifactType(app)
	RegisterPipelineArtifactType(app)
	return app
}

func TestPipelineArtifact_ExportRecipeShape(t *testing.T) {
	app := pipelineArtifactApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	agent, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Summarizer", OrchestratorPrompt: "sum"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	def := SavePipelineDef(udb, PipelineDef{
		Owner: "alice", Name: "Digest",
		Stages: []PipelineStage{
			{Name: "gather", Kind: StageWorker, Prompt: "collect {input}"},
			{Name: "sum", Kind: StageAgent, Prompt: "{prev}", Agent: agent.ID},
		},
	})

	p := &pipelineArtifact{app: app}
	raw, err := p.ExportArtifact(root, "Digest", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got PipelineDef
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if got.ID != def.ID {
		t.Fatalf("ID must travel (it's the cross-artifact reference key), got %q want %q", got.ID, def.ID)
	}
	if got.Owner != "" || !got.Created.IsZero() || !got.Updated.IsZero() {
		t.Fatalf("owner/timestamps must not travel: %+v", got)
	}
	if got.Stages[1].Agent != "Summarizer" {
		t.Fatalf("agent ID ref must normalize to the agent's name, got %q", got.Stages[1].Agent)
	}
	// Normalization operates on a copy — the stored def keeps its ID ref.
	stored, _ := LoadPipelineDef(udb, "alice", def.ID)
	if stored.Stages[1].Agent != agent.ID {
		t.Fatalf("export must not mutate the stored def: %+v", stored.Stages[1])
	}
	// Export also resolves by ID — the form the dependency closure and the
	// existence probe use.
	if _, err := p.ExportArtifact(root, def.ID, "alice"); err != nil {
		t.Fatalf("export by ID: %v", err)
	}
}

func TestPipelineArtifact_ImportPreservesIDSkipsCollisions(t *testing.T) {
	app := pipelineArtifactApp(t)
	p := &pipelineArtifact{app: app}
	stages := []PipelineStage{{Name: "s1", Kind: StageWorker, Prompt: "x"}}
	recipe, _ := json.Marshal(PipelineDef{ID: "pipe-7", Name: "Digest", Stages: stages})

	name, skip, err := p.ImportArtifact(nil, recipe, "bob")
	if err != nil || skip != "" || name != "Digest" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	udb := UserDB(app.DB, "bob")
	got, ok := LoadPipelineDef(udb, "bob", "pipe-7")
	if !ok {
		t.Fatal("traveled ID must be preserved on import")
	}
	if got.Owner != "bob" || got.Created.IsZero() {
		t.Fatalf("owner/created must be the importer's: %+v", got)
	}

	// Same ID again → skip; same name (case-insensitive) under a fresh ID →
	// skip too.
	_, skip, err = p.ImportArtifact(nil, recipe, "bob")
	if err != nil || !strings.Contains(skip, "id already exists") {
		t.Fatalf("same-id import must skip: skip=%q err=%v", skip, err)
	}
	renamed, _ := json.Marshal(PipelineDef{ID: "pipe-8", Name: "digest", Stages: stages})
	_, skip, err = p.ImportArtifact(nil, renamed, "bob")
	if err != nil || !strings.Contains(skip, "name already exists") {
		t.Fatalf("same-name import must skip: skip=%q err=%v", skip, err)
	}

	// A recipe that fails validation is a hard error, not a silent save.
	bad, _ := json.Marshal(PipelineDef{Name: "Empty"})
	if _, _, err = p.ImportArtifact(nil, bad, "bob"); err == nil {
		t.Fatal("no-stage recipe must fail validation")
	}
}

func TestPipelineClosure_CarriesStageAgent(t *testing.T) {
	app := pipelineArtifactApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	agent, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "researcher", OrchestratorPrompt: "r"})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	SavePipelineDef(udb, PipelineDef{
		Owner: "alice", Name: "Research Digest",
		Stages: []PipelineStage{{Name: "dig", Kind: StageAgent, Prompt: "{input}", Agent: agent.ID}},
	})

	b, err := ExportArtifactBundle(root, []ArtifactSel{{Type: "pipeline", Name: "Research Digest", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "pipeline" || b.Artifacts[1].Type != "agent" {
		t.Fatalf("expected [pipeline, agent], got %+v", b.Artifacts)
	}
	if b.Artifacts[1].Name != "researcher" {
		t.Fatalf("stage's ID ref must fold in the agent by name: %+v", b.Artifacts[1])
	}
}

func TestAgentClosure_CarriesAttachedPipeline(t *testing.T) {
	app := pipelineArtifactApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	def := SavePipelineDef(udb, PipelineDef{
		Owner: "alice", Name: "Digest",
		Stages: []PipelineStage{{Name: "s1", Kind: StageWorker, Prompt: "x"}},
	})
	if _, err := saveAgent(udb, AgentRecord{
		Owner: "alice", Name: "editor", OrchestratorPrompt: "e",
		AttachedPipelines: []string{def.ID},
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	b, err := ExportArtifactBundle(root, []ArtifactSel{{Type: "agent", Name: "editor", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "agent" || b.Artifacts[1].Type != "pipeline" {
		t.Fatalf("expected [agent, pipeline], got %+v", b.Artifacts)
	}
	var pd PipelineDef
	if err := json.Unmarshal(b.Artifacts[1].Recipe, &pd); err != nil {
		t.Fatalf("pipeline recipe: %v", err)
	}
	if pd.ID != def.ID {
		t.Fatalf("pipeline must travel with preserved ID (the agent's reference key): %+v", pd)
	}

	// On a fresh install, preview must see the agent's ID-based pipeline
	// reference satisfied by the bundle itself, and import must land both
	// with the wiring intact.
	fresh := pipelineArtifactApp(t)
	data, _ := json.Marshal(b)
	res, err := PreviewArtifactBundle(&DBase{Store: kvlite.MemStore()}, data, "bob")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if res.WouldImport != 2 {
		t.Fatalf("both artifacts should predict import: %+v", res.Items)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("in-bundle pipeline (referenced by ID) must not warn: %v", res.Warnings)
	}
	ires, err := ImportArtifactBundle(&DBase{Store: kvlite.MemStore()}, data, "bob")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if ires.Imported != 2 || len(ires.Warnings) != 0 {
		t.Fatalf("expected 2 imports with no warnings: %+v", ires)
	}
	fudb := UserDB(fresh.DB, "bob")
	if _, ok := LoadPipelineDef(fudb, "bob", def.ID); !ok {
		t.Fatal("imported pipeline must keep the traveled ID")
	}
	imported, ok := findAgentByNameOrID(fudb, "bob", "editor")
	if !ok || len(imported.AttachedPipelines) != 1 || imported.AttachedPipelines[0] != def.ID {
		t.Fatalf("imported agent must still reference the pipeline by its traveled ID: %+v", imported.AttachedPipelines)
	}
}
