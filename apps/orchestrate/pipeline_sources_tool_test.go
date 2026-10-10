package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// Builder writes cite and verify as JSON; both must survive the parse, and the
// help must say how sources, citing and checking fit together.
func TestThePipelineToolTakesCiteAndVerify(t *testing.T) {
	stages, err := parsePipelineStages([]any{
		map[string]any{"name": "write", "prompt": "Cite from {sources}", "cite": true},
		map[string]any{"name": "check", "kind": "verify", "check": " write "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stages[0].Cite || stages[1].Kind != "verify" || stages[1].Check != "write" {
		t.Fatalf("cite/verify lost in the parse: %+v", stages)
	}
	for _, want := range []string{"=== SOURCES ===", "{sources}", `"kind": "verify"`, "unverified_figures"} {
		if !strings.Contains(pipelineHelpText, want) {
			t.Errorf("the help must cover %q", want)
		}
	}
}

// Builder writes gather, a panel's research and count_from as JSON; all three
// must survive the parse (count_from was documented and silently dropped), and
// the help must say how gathering works.
func TestThePipelineToolTakesGatherAndResearch(t *testing.T) {
	stages, err := parsePipelineStages([]any{
		map[string]any{"name": "look", "kind": "gather", "prompt": "solar cost", "count": float64(4), "count_from": " {pages} "},
		map[string]any{"name": "debate", "kind": "panel", "panel": []any{"Pro", "Con"}, "research": float64(2), "prompt": "{research}"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stages[0].Kind != "gather" || stages[0].Count != 4 || stages[0].CountFrom != "{pages}" || stages[1].Research != 2 {
		t.Fatalf("gather/research/count_from lost in the parse: %+v", stages)
	}
	for _, want := range []string{`"kind": "gather"`, `"research": 2`, "{research}", "count_from (panel, loop, gather)"} {
		if !strings.Contains(pipelineHelpText, want) {
			t.Errorf("the help must cover %q", want)
		}
	}
}

// A gather or a researching panel names no tools, but a run started from a
// pipeline page or an app gets only the tools stages name: they must be named
// for it, across the follow-ups and the suggest pipeline too.
func TestStandaloneRunsGetTheToolsGatheringReadsWith(t *testing.T) {
	plain := []PipelineStage{{Name: "w", Prompt: "x"}}
	for name, def := range map[string]PipelineDef{
		"a gather":            {Stages: []PipelineStage{{Name: "g", Kind: StageGather}}},
		"a researching panel": {Stages: []PipelineStage{{Name: "p", Kind: StagePanel, Panel: []string{"a", "b"}, Research: 1}}},
		"a follow-up gather":  {Stages: plain, FollowUps: []PipelineDef{{Name: "f", Stages: []PipelineStage{{Name: "g", Kind: StageGather}}}}},
		"a suggest gather":    {Stages: plain, Suggest: &PipelineDef{Stages: []PipelineStage{{Name: "g", Kind: StageGather}}}},
	} {
		got := strings.Join(pipelineDeclaredToolNames(def), ",")
		if got != "fetch_url,web_search" {
			t.Errorf("%s must bring web_search and fetch_url, got %q", name, got)
		}
	}
	if got := pipelineDeclaredToolNames(PipelineDef{Stages: plain}); len(got) != 0 {
		t.Errorf("a stage that names nothing still gets nothing: %v", got)
	}
}

// Builder writes the suggest recipe as JSON; {} removes it.
func TestThePipelineToolTakesSuggest(t *testing.T) {
	sg, err := parsePipelineSuggest(map[string]any{"name": "Find a topic", "stages": []any{
		map[string]any{"name": "news", "kind": "gather", "prompt": "top stories today"},
		map[string]any{"name": "pick", "prompt": "Five questions from {stage:news}"},
	}})
	if err != nil || sg == nil || sg.Name != "Find a topic" || len(sg.Stages) != 2 || sg.Stages[0].Kind != StageGather {
		t.Fatalf("suggest lost in the parse: %+v %v", sg, err)
	}
	if sg, err := parsePipelineSuggest(map[string]any{}); sg != nil || err != nil {
		t.Errorf("{} removes it: %+v %v", sg, err)
	}
	if !strings.Contains(pipelineHelpText, "=== SUGGEST ===") {
		t.Error("the help must cover suggest")
	}
}
