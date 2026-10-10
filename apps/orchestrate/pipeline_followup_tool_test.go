package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// Builder declares follow-ups as data; they have to survive the parse, show in
// what it reads back, and be served on the pipeline's own page.
func TestThePipelineToolTakesFollowUps(t *testing.T) {
	fus, err := parsePipelineFollowUps([]any{map[string]any{
		"name": "Write report", "description": "A briefing",
		"stages": []any{map[string]any{"name": "w", "prompt": "Write a briefing from {input}"}},
	}})
	if err != nil || len(fus) != 1 || fus[0].Name != "Write report" || len(fus[0].Stages) != 1 {
		t.Fatalf("follow-ups lost in the parse: %+v %v", fus, err)
	}
	if _, err := parsePipelineFollowUps("not a list"); err == nil {
		t.Error("a follow-up list that is not a list must be refused")
	}
	slim := string(slimPipelineJSON(PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s"}}, FollowUps: fus}))
	if !strings.Contains(slim, `"followups":[{"name":"Write report"`) {
		t.Errorf("get must show the follow-ups: %s", slim)
	}
	if !strings.Contains(pipelineHelpText, "=== FOLLOW-UPS ===") || !strings.Contains(pipelineHelpText, "{children}") {
		t.Error("the help must document follow-ups and what they read")
	}
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !strings.Contains(read("pipelines_http.go"), `strings.CutPrefix(action, "followup/")`) ||
		!strings.Contains(read("pipeline_page.go"), `FollowUpsURL: "api/pipelines/" + url_(def.ID) + "/followups"`) ||
		!strings.Contains(read("app_def_tool_build.go"), `FollowUpsURL: "pipeline/followups"`) {
		t.Error("the pipeline page and app sections must offer follow-ups, and the routes must reach them")
	}
}
