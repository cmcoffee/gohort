package orchestrate

// An ask_user question that arrives as a list, the question first and its
// choices after it, is drawn as a question with buttons rather than as raw
// JSON. Observed from a model that sent the whole multiple-choice question as
// one JSON-encoded string under "questions".

import (
	"strings"
	"testing"
)

func TestAQuestionSentAsAListKeepsItsChoices(t *testing.T) {
	q, opts := askQuestionArgs(map[string]any{
		"questions": `["Which best describes where the pain is?", "upper right side, under your ribs", "lower right side", "it moves around"]`,
	})
	if q != "Which best describes where the pain is?" || len(opts) != 3 || opts[1] != "lower right side" {
		t.Errorf("a JSON-encoded list: question %q, options %v", q, opts)
	}

	q, opts = askQuestionArgs(map[string]any{"question": []any{"Which repo?", "api", "web"}})
	if q != "Which repo?" || len(opts) != 2 {
		t.Errorf("a real list: question %q, options %v", q, opts)
	}

	// Choices given as options win over the list's.
	_, opts = askQuestionArgs(map[string]any{"question": `["Pick one?", "a", "b"]`, "options": []any{"x", "y", "z"}})
	if strings.Join(opts, ",") != "x,y,z" {
		t.Errorf("options should win: %v", opts)
	}

	// Several questions in a list are one question in parts, not a question
	// and its choices.
	q, opts = askQuestionArgs(map[string]any{"questions": []any{"Where does it hurt?", "Since when?"}})
	if q != "Where does it hurt?\nSince when?" || len(opts) != 0 {
		t.Errorf("several questions: question %q, options %v", q, opts)
	}

	// The documented shape, and a bracket that is just text, are unchanged.
	if q, opts = askQuestionArgs(map[string]any{"question": "Which feed?", "options": []any{"news", "sports"}}); q != "Which feed?" || len(opts) != 2 {
		t.Errorf("the documented shape: %q %v", q, opts)
	}
	if q, _ = askQuestionArgs(map[string]any{"question": "[draft] ok to send?"}); q != "[draft] ok to send?" {
		t.Errorf("a bracket in prose was read as a list: %q", q)
	}
}
