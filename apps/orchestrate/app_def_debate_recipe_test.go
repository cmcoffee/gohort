package orchestrate

import (
	"encoding/json"
	"os"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// extras/debate.pipeline.json + extras/debate.app.json are a declarative
// rebuild of private/debate, which is 13k lines of Go. They are shipped as
// examples, so they have to actually be authorable: a recipe in extras/ that
// the framework would refuse is worse than no example, because the reader
// assumes their own mistake.
//
// This drives them through the real validators. It cannot run a debate — that
// needs a model — but everything that fails at AUTHORING time fails here.

func readExtras(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile("../../extras/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
}

func TestDebateRecipeIsARunnablePipeline(t *testing.T) {
	var def PipelineDef
	readExtras(t, "debate.pipeline.json", &def)

	if err := def.Validate(); err != nil {
		t.Fatalf("the shipped debate pipeline is not runnable: %v", err)
	}

	// The shape that makes it a debate rather than a report: two voices on the
	// same question over more than one round. One round is a poll — nobody has
	// replied to anybody until the second.
	var panel PipelineStage
	for _, s := range def.Stages {
		if s.Kind == StagePanel {
			panel = s
		}
	}
	if len(panel.Panel) != 2 || panel.Count < 2 {
		t.Errorf("panel stage = %d voices over %d rounds; a debate needs two voices and at least two rounds", len(panel.Panel), panel.Count)
	}

	// The judge has to return a decision the sidebar can carry, not prose.
	var judge PipelineStage
	for _, s := range def.Stages {
		if s.Name == "judge" {
			judge = s
		}
	}
	fields := map[string]PipelineField{}
	for _, f := range judge.Output {
		fields[f.Name] = f
	}
	if len(fields["winner"].Enum) == 0 || len(fields["confidence"].Enum) == 0 {
		t.Error("winner and confidence must be enums — an open string is how a pill ends up with a value no variant matches")
	}
}

func TestDebateRecipeBuildsTheApp(t *testing.T) {
	var def PipelineDef
	readExtras(t, "debate.pipeline.json", &def)

	var app struct {
		Name        string          `json:"name"`
		Slug        string          `json:"slug"`
		RecordKey   string          `json:"record_key"`
		PipelineID  string          `json:"pipeline_id"`
		DataSources json.RawMessage `json:"data_sources"`
		Sections    []any           `json:"sections"`
	}
	readExtras(t, "debate.app.json", &app)

	spec := AppSpec{
		Slug: app.Slug, Name: app.Name, RecordKey: app.RecordKey,
		PipelineID: app.PipelineID,
	}

	page, err := buildAppPage(spec, app.Sections)
	if err != nil {
		t.Fatalf("the shipped debate app does not build: %v", err)
	}
	if len(page.Sections) != 1 {
		t.Fatalf("the debate app is one pipeline section and nothing else, got %d sections", len(page.Sections))
	}
	panel, ok := page.Sections[0].Body.(ui.PipelinePanel)
	if !ok {
		t.Fatalf("the section must be the pipeline panel; got %T", page.Sections[0].Body)
	}
	if panel.CancelURL == "" || panel.ReconnectURL == "" || panel.FollowUpsURL == "" {
		t.Error("the panel should carry cancel, reconnect and the follow-ups/suggest base without the author asking")
	}
	// Suggest is the pipeline's own recipe, found through the surface, so the
	// page names no script for it.
	if panel.PrefillURL != "" {
		t.Errorf("suggest should come from the pipeline, not %q", panel.PrefillURL)
	}
	if def.Suggest == nil {
		t.Error("the pipeline should carry the suggest recipe the page relies on")
	}
	if len(panel.Actions) != 1 || len(panel.SessionMetaFields) != 3 {
		t.Errorf("got %d toolbar buttons and %d meta fields, want 1 and 3", len(panel.Actions), len(panel.SessionMetaFields))
	}

	// The join between the two files: every pill the app draws has to be a
	// field the pipeline promotes, or the sidebar renders blank.
	if notes := appSessionMetaNotes(app.Sections, def.SessionMeta); len(notes) != 0 {
		t.Errorf("the app draws meta the pipeline does not promote: %v", notes)
	}
}

// The point of the recipe: a debate is DATA. No html section, no script, no
// tool only debate has; everything it shows and checks is a primitive any
// pipeline has.
func TestDebateRecipeIsPureData(t *testing.T) {
	raw, err := os.ReadFile("../../extras/debate.app.json")
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Sections    []map[string]any `json:"sections"`
		DataSources []any            `json:"data_sources"`
		Actions     []any            `json:"actions"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if len(app.DataSources) > 0 || len(app.Actions) > 0 {
		t.Error("the debate app should need no scripts")
	}
	var sections []any
	for _, s := range app.Sections {
		sections = append(sections, s)
	}
	if notes := appShapeNotes(sections, true); len(notes) != 0 {
		t.Errorf("the shipped app trips its own authoring notes: %v", notes)
	}
	for _, s := range app.Sections {
		if s["kind"] == "html" {
			t.Error("the debate app should need no html: its cards are the stages' own")
		}
	}
	var def PipelineDef
	readExtras(t, "debate.pipeline.json", &def)
	kinds := map[PipelineStageKind]int{}
	for _, s := range def.Stages {
		kinds[s.Kind]++
		if s.Kind == StageTool {
			t.Errorf("stage %s calls a tool: the recipe must not depend on a tool only debate has", s.Name)
		}
	}
	if kinds[StageGather] == 0 || kinds[StageVerify] == 0 || kinds[StagePanel] == 0 {
		t.Errorf("a debate on evidence gathers, argues and checks: got %v", kinds)
	}
}
