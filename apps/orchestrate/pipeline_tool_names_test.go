package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A stage's tool list is the same exact-name filter a machine step's is —
// literally the same function — and the machine editor has caught a typo in
// one since the class was first seen. A pipeline had no such check, so the
// identical mistake failed identically and in silence, hours later, on a turn
// nobody was watching.
func TestAStageNamingAToolNobodyHasIsReportedAtSaveTime(t *testing.T) {
	// A catalog as a caller would hold it, written out by hand: what the
	// test binary happens to have registered is not the subject here.
	known := map[string]bool{"web_search": true, "fetch_url": true, "search_prod_logs": true}

	def := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", Tools: []string{"web_search", "Web_Search"}},
	}}
	found := strings.Join(stageToolFindings(def, known), "\n")
	if !strings.Contains(found, "Web_Search") {
		t.Fatalf("the misnamed tool should be reported: %q", found)
	}
	// The recoverable class is mechanical, not imaginative: the case the
	// author had, the separator they used, the raw remote name before a
	// server prefixed it. A guessed spelling costs more than no guess.
	if !strings.Contains(found, `did you mean "web_search"?`) {
		t.Errorf("a case mismatch should carry its correction: %q", found)
	}
	if strings.Contains(found, "\"web_search\" is not") {
		t.Errorf("the correct name should not be flagged: %q", found)
	}

	// A typo one level down is still a typo: loop and fanout bodies are
	// checked like any other stage.
	nested := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "each", Kind: StageLoop, Count: 2, Body: []PipelineStage{
			{Name: "inner", Kind: StageWorker, Prompt: "look", Tools: []string{"Web_Search"}},
		}},
	}}
	if got := strings.Join(stageToolFindings(nested, known), "\n"); !strings.Contains(got, "stage inner") {
		t.Errorf("a nested stage's names must be checked too: %q", got)
	}

	// The whole list missing is its own finding, because the consequence
	// differs in kind: the stage runs tool-less rather than narrowed.
	allBad := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker, Prompt: "look", Tools: []string{"nope_one", "nope_two"}},
	}}
	if got := strings.Join(stageToolFindings(allBad, known), "\n"); !strings.Contains(got, "runs with NO tools") {
		t.Errorf("a total miss should say what the stage actually gets: %q", got)
	}

	// The explicit nothing is a setting, not a name, and must never be
	// reported as one.
	marker := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "write", Kind: StageSynthesize, Prompt: "write it", Tools: []string{NoToolsMarker}},
	}}
	if got := stageToolFindings(marker, known); len(got) != 0 {
		t.Errorf("the no-tools marker was reported as an unresolvable tool: %v", got)
	}

	// And a pipeline naming nothing costs nothing — no agent walk, no catalog.
	quiet := PipelineDef{Name: "research", Owner: "u", Stages: []PipelineStage{
		{Name: "write", Kind: StageWorker, Prompt: "write it"},
	}}
	if got := stageToolFindings(quiet, known); got != nil {
		t.Errorf("a pipeline that names no tools has nothing to check: %v", got)
	}
}
