package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/extras"
)

// Builder starts a debate or a research build from a shipped recipe: the list
// names them with their shape, and one comes back whole with its app.
func TestThePipelineToolHandsOutTheShippedRecipes(t *testing.T) {
	list, err := pipelineExamples()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- debate (Debate)", "- research (Deep Research)", "evidence(gather)", "dig(fanout)[look(gather) -> notes(worker)]", "app: debate.app.json"} {
		if !strings.Contains(list, want) {
			t.Errorf("the examples list must show %q:\n%s", want, list)
		}
	}
	got, err := pipelineExample(map[string]any{"name": "Debate"})
	if err != nil || !strings.Contains(got, `"kind": "verify"`) || !strings.Contains(got, "debate.app.json") {
		t.Errorf("example debate must return the recipe and its app: %v\n%.300s", err, got)
	}
	if _, err := pipelineExample(map[string]any{"name": "nope"}); err == nil || !strings.Contains(err.Error(), "research") {
		t.Errorf("an unknown name must list what there is: %v", err)
	}
}

// Every shipped recipe is one the framework accepts, and every shipped app
// builds against the pipeline it ships beside: an example that fails its own
// validators is worse than none, because the reader assumes the mistake is
// theirs.
func TestEveryShippedRecipeIsAuthorable(t *testing.T) {
	apps := 0
	for _, name := range extras.Recipes() {
		if !strings.HasSuffix(name, ".pipeline.json") {
			continue
		}
		def, ok := shippedPipeline(name)
		if !ok {
			t.Errorf("%s does not decode as a pipeline", name)
			continue
		}
		if err := def.Validate(); err != nil {
			t.Errorf("%s is not runnable: %v", name, err)
		}
		raw, ok := extras.Recipe(companionApp(name))
		if !ok {
			continue
		}
		apps++
		var app struct {
			Name       string `json:"name"`
			Slug       string `json:"slug"`
			RecordKey  string `json:"record_key"`
			PipelineID string `json:"pipeline_id"`
			Sections   []any  `json:"sections"`
		}
		if err := json.Unmarshal(raw, &app); err != nil {
			t.Errorf("%s: %v", companionApp(name), err)
			continue
		}
		if app.PipelineID != def.Name {
			t.Errorf("%s binds %q, but the pipeline beside it is %q", companionApp(name), app.PipelineID, def.Name)
		}
		spec := AppSpec{Slug: app.Slug, Name: app.Name, RecordKey: app.RecordKey, PipelineID: app.PipelineID}
		if _, err := buildAppPage(spec, app.Sections); err != nil {
			t.Errorf("%s does not build: %v", companionApp(name), err)
		}
		if notes := appSessionMetaNotes(app.Sections, def.SessionMeta); len(notes) != 0 {
			t.Errorf("%s draws meta its pipeline does not promote: %v", companionApp(name), notes)
		}
		if notes := appShapeNotes(app.Sections, true); len(notes) != 0 {
			t.Errorf("%s trips its own authoring notes: %v", companionApp(name), notes)
		}
	}
	if apps < 2 {
		t.Errorf("the debate and research apps should both ship, found %d", apps)
	}
}
