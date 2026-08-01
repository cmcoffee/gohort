package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A multi-stage recipe was only ever reachable as a tool an agent happened to
// own — the panel that renders a run and the endpoints that serve one both
// existed, with nothing able to mount them. These cover the connector: a
// section that compiles to that panel, bound to a pipeline, wired to endpoints
// relative to the app.

func TestBuildAppPage_PipelineSection(t *testing.T) {
	spec := AppSpec{Slug: "market-scan", Name: "Market Scan", RecordKey: "id", PipelineID: "pl-1"}
	page, err := buildAppPage(spec, []any{
		map[string]any{
			"kind":         "pipeline",
			"title":        "Run a scan",
			"submit_label": "Scan",
			"fields": []any{
				map[string]any{"name": "topic", "label": "Question", "type": "textarea", "rows": float64(4), "required": true},
			},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	body, err := page.ConfigJSON()
	if err != nil {
		t.Fatalf("config json: %v", err)
	}
	var cfg struct {
		Sections []struct {
			Body map[string]any `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Sections) != 1 {
		t.Fatalf("want 1 section, got %d", len(cfg.Sections))
	}
	got := cfg.Sections[0].Body
	// Every endpoint has to be RELATIVE to the app mount: the panel is served
	// under /custom/<slug>/, and an absolute /api/pipelines/... would both leave
	// the app's namespace and bypass the owner-resolves-def rule.
	for key, want := range map[string]string{
		"submit_url":         "pipeline/stream",
		"sessions_list_url":  "pipeline/sessions",
		"session_load_url":   "pipeline/sessions/{id}",
		"session_delete_url": "pipeline/sessions/{id}",
		"submit_label":       "Scan",
	} {
		if s, _ := got[key].(string); s != want {
			t.Errorf("%s = %q, want %q", key, s, want)
		}
	}
	fields, _ := got["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("want the authored field, got %v", got["fields"])
	}
	f, _ := fields[0].(map[string]any)
	if f["name"] != "topic" || f["type"] != "textarea" {
		t.Errorf("field did not survive authoring: %v", f)
	}
	if f["required"] != true {
		t.Errorf("required was dropped: %v", f)
	}
}

// The default submit field is not cosmetic: the run surface reads input|topic,
// so a section authored without fields still has to produce one the run can
// actually consume.
func TestPipelineSectionDefaultsToAConsumableField(t *testing.T) {
	spec := AppSpec{Slug: "s", Name: "S", RecordKey: "id", PipelineID: "pl-1"}
	sec, err := buildAppSection(spec, map[string]any{"kind": "pipeline"}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	panel, ok := sec.Body.(ui.PipelinePanel)
	if !ok {
		t.Fatalf("want a pipeline panel, got %T", sec.Body)
	}
	if len(panel.Fields) != 1 || panel.Fields[0].Name != "topic" {
		t.Fatalf("default field must be one the run reads (input|topic), got %+v", panel.Fields)
	}
	if !panel.Fields[0].Required {
		t.Error("a run with no input is not a run — the default field is required")
	}
}

// Binding first, section second. Without the pipeline_id the panel would render
// a submit button wired to an endpoint that 404s, which reads to the user as a
// broken app rather than an unfinished one.
func TestPipelineSectionRefusesWithoutABinding(t *testing.T) {
	spec := AppSpec{Slug: "s", Name: "S", RecordKey: "id"}
	_, err := buildAppSection(spec, map[string]any{"kind": "pipeline"}, nil)
	if err == nil {
		t.Fatal("a pipeline section with no pipeline_id must be refused at authoring time")
	}
	if !strings.Contains(err.Error(), "pipeline_id") {
		t.Errorf("the error should name what is missing, got %q", err)
	}
}

// A page-launched run has no calling agent, so it used to inherit an empty tool
// catalog: a stage declaring web_search silently took the tool-less path and
// answered from the model alone. The definition supplies the pool now.
func TestStandaloneRunResolvesTheToolsItsStagesDeclare(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root

	def := PipelineDef{
		Name: "research",
		Stages: []PipelineStage{
			{Name: "decompose", Kind: StageWorker, Prompt: "split {input}"},
			{Name: "dig", Kind: StageWorker, Prompt: "answer", Tools: []string{"web_search", "no_such_tool"}},
			{Name: "loop", Kind: StageLoop, Count: 2, Body: []PipelineStage{
				// Declared only inside the loop, and only as a tool stage's own
				// Tool — the two ways a name hides from a shallow walk.
				{Name: "inner", Kind: StageTool, Tool: "date_math"},
			}},
		},
	}
	// What the run asks the registry for. Sorted and deduped, and it must reach
	// INTO the loop body — a refinement pipeline keeps its tool-calling stages
	// there, so a walk that stopped at the top level would leave exactly the
	// iterative ones tool-less.
	got := pipelineDeclaredToolNames(def)
	want := []string{"date_math", "no_such_tool", "web_search"}
	if len(got) != len(want) {
		t.Fatalf("declared names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("declared names = %v, want %v (sorted, deduped)", got, want)
		}
	}
	// A stage that declared nothing still gets nothing — the per-stage contract
	// is the author's, and this fix must not quietly widen it.
	bare := PipelineDef{Name: "bare", Stages: []PipelineStage{{Name: "x", Kind: StageWorker, Prompt: "hi"}}}
	if names := pipelineDeclaredToolNames(bare); names != nil {
		t.Errorf("a pipeline declaring no tools must ask for none, got %v", names)
	}
	if tools := app.pipelineStandaloneTools(context.Background(), "u", bare); tools != nil {
		t.Errorf("a pipeline declaring no tools must inherit none, got %v", tools)
	}
	// Nothing resolves in a bare test registry — the point is that unresolvable
	// names are skipped rather than fabricated into a catalog entry whose call
	// would fail deep inside a stage.
	if tools := app.pipelineStandaloneTools(context.Background(), "u", def); len(tools) != 0 {
		t.Errorf("names that resolve to no registered tool must be dropped, got %d", len(tools))
	}
}

// The binding resolves by name as well as id: an imported bundle carries the
// reference as a name because the pipeline is reborn under a fresh id.
func TestLookupAppPipelineByNameOrID(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	udb := UserDB(root, "u")

	saved := SavePipelineDef(udb, PipelineDef{
		Name: "Market Scan", Owner: "u",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "hi"}},
	})
	if got, ok := app.LookupAppPipeline("u", saved.ID); !ok || got.ID != saved.ID {
		t.Errorf("id lookup failed: %v %v", got.ID, ok)
	}
	if got, ok := app.LookupAppPipeline("u", "Market Scan"); !ok || got.ID != saved.ID {
		t.Errorf("name lookup failed: %v %v", got.ID, ok)
	}
	if got, ok := app.LookupAppPipeline("u", "market_scan"); !ok || got.ID != saved.ID {
		t.Errorf("snake-cased name lookup failed: %v %v", got.ID, ok)
	}
	if _, ok := app.LookupAppPipeline("u", "nothing-like-this"); ok {
		t.Error("an unknown name must not resolve to some other pipeline")
	}
	// Another user's store is a different namespace, not a fallback.
	if _, ok := app.LookupAppPipeline("someone-else", saved.ID); ok {
		t.Error("a pipeline resolved for the wrong owner is a cross-user leak")
	}
}
