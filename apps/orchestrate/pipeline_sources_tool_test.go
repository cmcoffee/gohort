package orchestrate

import (
	"strings"
	"testing"
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
