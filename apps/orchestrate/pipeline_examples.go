package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/extras"
)

// The shipped recipes, for Builder to start from. A debate or a deep-research
// build is the same few stages wired the same way every time; written from
// the field reference alone it comes out a different shape each build, and
// usually without the parts that make it trustworthy (shared evidence, cited
// claims, the check). Starting from a recipe that runs, and adapting it to the
// person's question, is how those parts survive.

// pipelineExamples answers pipeline(action="examples"): each shipped
// pipeline, what it is for, its shape, and the app that runs it when one
// ships beside it.
func pipelineExamples() (string, error) {
	var b strings.Builder
	b.WriteString("Shipped pipelines to start from. Read one with action=\"example\", name=<file>, then adapt it: the person's question, their names and fields, their sides or sub-questions. Keep the shape that makes it work.\n")
	for _, name := range extras.Recipes() {
		if !strings.HasSuffix(name, ".pipeline.json") {
			continue
		}
		def, ok := shippedPipeline(name)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n- %s (%s): %s\n  stages: %s", strings.TrimSuffix(name, ".pipeline.json"), def.Name, def.Description, stageShape(def.Stages))
		if def.Suggest != nil {
			b.WriteString(" | suggest")
		}
		if len(def.FollowUps) > 0 {
			b.WriteString(" | follow-ups")
		}
		if _, ok := extras.Recipe(companionApp(name)); ok {
			b.WriteString("\n  app: " + companionApp(name) + " (app_def create args)")
		}
	}
	return b.String(), nil
}

// pipelineExample answers pipeline(action="example", name=<debate|debate.pipeline.json>):
// the recipe in the shape create takes, and its app when one ships.
func pipelineExample(args map[string]any) (string, error) {
	want := strings.TrimSpace(strings.ToLower(stringArg(args, "name")))
	want = strings.TrimSuffix(want, ".json")
	want = strings.TrimSuffix(want, ".pipeline")
	file := want + ".pipeline.json"
	raw, ok := extras.Recipe(file)
	if !ok || want == "" {
		list, _ := pipelineExamples()
		return "", fmt.Errorf("no shipped pipeline called %q.\n%s", want, list)
	}
	var b strings.Builder
	b.WriteString("Shipped pipeline " + file + ". To build from it, pass its stages (and suggest / followups / session_meta when it has them) to action=\"create\" with your own name and description, after adapting the prompts to the person's question. It is a recipe, not a template with blanks: every prompt says why it asks what it asks, so keep the reasons when you change the words.\n\n")
	b.Write(raw)
	if app, ok := extras.Recipe(companionApp(file)); ok {
		b.WriteString("\n\nThe app that runs it (" + companionApp(file) + "): app_def(action=\"create\") arguments. Set pipeline_id to YOUR pipeline's name, and keep the fields' names in step with what the prompts read ({field_name}).\n\n")
		b.Write(app)
	}
	return b.String(), nil
}

// shippedPipeline decodes one shipped pipeline file.
func shippedPipeline(name string) (PipelineDef, bool) {
	raw, ok := extras.Recipe(name)
	if !ok {
		return PipelineDef{}, false
	}
	var def PipelineDef
	if json.Unmarshal(raw, &def) != nil {
		return PipelineDef{}, false
	}
	return def, true
}

// companionApp is the app file that ships beside a pipeline file.
func companionApp(pipelineFile string) string {
	return strings.TrimSuffix(pipelineFile, ".pipeline.json") + ".app.json"
}

// stageShape is a stage list in one line: each stage's name and kind, a body
// in brackets.
func stageShape(stages []PipelineStage) string {
	parts := make([]string, 0, len(stages))
	for _, s := range stages {
		kind := string(s.Kind)
		if kind == "" {
			kind = string(StageWorker)
		}
		p := s.Name + "(" + kind + ")"
		if len(s.Body) > 0 {
			p += "[" + stageShape(s.Body) + "]"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " -> ")
}
