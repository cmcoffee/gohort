package orchestrate

import (
	"encoding/json"
	"os"
	"strings"
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

	var raw any
	if err := json.Unmarshal(app.DataSources, &raw); err != nil {
		t.Fatalf("data_sources: %v", err)
	}
	sources, _ := appDataSources(raw)
	spec := AppSpec{
		Slug: app.Slug, Name: app.Name, RecordKey: app.RecordKey,
		PipelineID: app.PipelineID, DataSources: sources,
	}

	page, err := buildAppPage(spec, app.Sections)
	if err != nil {
		t.Fatalf("the shipped debate app does not build: %v", err)
	}

	// The html section registers a block renderer, and a pipeline panel COPIES
	// the registry when it mounts. Ordering is the whole correctness of this
	// file and it fails silently when it is wrong.
	if len(page.Sections) < 2 {
		t.Fatalf("expected an html section and a pipeline section, got %d", len(page.Sections))
	}
	if _, ok := page.Sections[0].Body.(ui.Card); !ok {
		t.Fatalf("section 1 must be the html FRAGMENT that registers the renderer (a ui.Card); got %T", page.Sections[0].Body)
	}
	panel, ok := page.Sections[1].Body.(ui.PipelinePanel)
	if !ok {
		t.Fatalf("section 2 must be the pipeline panel; got %T", page.Sections[1].Body)
	}

	// Everything the last four commits added, wired by one authored file.
	if panel.CancelURL == "" || panel.ReconnectURL == "" {
		t.Error("the panel should carry cancel + reconnect without the author asking")
	}
	if panel.PrefillURL != "data/topic-ideas" {
		t.Errorf("suggest is bound to %q, want the slugified data source", panel.PrefillURL)
	}
	if len(panel.Actions) != 2 || len(panel.SessionMetaFields) != 3 {
		t.Errorf("got %d toolbar buttons and %d meta fields, want 2 and 3", len(panel.Actions), len(panel.SessionMetaFields))
	}

	// The join between the two files: every pill the app draws has to be a
	// field the pipeline promotes, or the sidebar renders blank.
	if notes := appSessionMetaNotes(app.Sections, def.SessionMeta); len(notes) != 0 {
		t.Errorf("the app draws meta the pipeline does not promote: %v", notes)
	}
}

// A client-method button needs something to register its handler. The recipe
// does it in the html section; this is the check that the two stay together.
func TestDebateRecipeRegistersItsOwnClientAction(t *testing.T) {
	raw, err := os.ReadFile("../../extras/debate.app.json")
	if err != nil {
		t.Fatal(err)
	}
	var app struct {
		Sections []any `json:"sections"`
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if notes := appShapeNotes(app.Sections, true); len(notes) != 0 {
		t.Errorf("the shipped app trips its own authoring notes: %v", notes)
	}
	if !strings.Contains(string(raw), "uiRegisterClientAction") {
		t.Error("the Export button dispatches to a handler nothing registers")
	}
}
