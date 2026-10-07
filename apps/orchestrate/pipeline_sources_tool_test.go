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
