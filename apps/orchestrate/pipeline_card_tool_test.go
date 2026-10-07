package orchestrate

import (
	"strings"
	"testing"
)

// Builder writes a stage as JSON; render and card have to survive the parse,
// or a card it designed would silently come out as text.
func TestThePipelineToolKeepsACardLayout(t *testing.T) {
	stages, err := parsePipelineStages([]any{map[string]any{
		"name": "judge", "kind": "worker", "prompt": "Judge it.",
		"output": []any{"verdict", "confidence"},
		"render": " Card ", "card": map[string]any{"title": "verdict", "badges": "confidence"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 || stages[0].Render != "card" || stages[0].Card["title"] != "verdict" || stages[0].Card["badges"] != "confidence" {
		t.Fatalf("render/card lost in the parse: %+v", stages)
	}
	// And Builder is told they exist, with a worked example.
	if !strings.Contains(pipelineHelpText, "=== CARDS ===") || !strings.Contains(pipelineHelpText, `"render": "card"`) {
		t.Error("the pipeline help must document cards")
	}
}
