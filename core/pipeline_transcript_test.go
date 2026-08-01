package core

import (
	"strings"
	"testing"
)

// A declared-output stage returns JSON — that is what fan_over, until, and
// {stage:NAME.field} read. The transcript is not one of those consumers: the
// first card of a research run showed its four sub-questions as a raw JSON
// object, which reads as a broken app even though the pipeline is working.
func TestTranscriptRendersDeclaredFieldsForAReader(t *testing.T) {
	stage := PipelineStage{
		Name: "decompose", Kind: StageWorker,
		Output: []PipelineField{{Name: "sub_questions", Type: FieldList}},
	}
	raw := `{"sub_questions":["Best taqueria per Yelp?","What do locals say?"]}`
	got := transcriptBody(stage, raw, map[string]any{
		"sub_questions": []any{"Best taqueria per Yelp?", "What do locals say?"},
	})
	if strings.Contains(got, "{") || strings.Contains(got, "\"sub_questions\"") {
		t.Errorf("the card must not show the JSON envelope, got:\n%s", got)
	}
	if !strings.Contains(got, "- Best taqueria per Yelp?") {
		t.Errorf("a list should read as bullets, got:\n%s", got)
	}
	// One declared field IS the stage's output; its name is already the card
	// title, so a label would say it twice.
	if strings.Contains(got, "Sub questions") {
		t.Errorf("a single-field stage needs no field label, got:\n%s", got)
	}
}

func TestTranscriptLabelsMultipleFieldsInDeclaredOrder(t *testing.T) {
	stage := PipelineStage{
		Name: "check", Kind: StageWorker,
		Output: []PipelineField{
			{Name: "verdict", Type: FieldString},
			{Name: "done", Type: FieldBool},
			{Name: "score", Type: FieldNumber},
		},
	}
	got := transcriptBody(stage, `{}`, map[string]any{
		"done": true, "verdict": "looks right", "score": float64(3),
	})
	vi, di, si := strings.Index(got, "Verdict"), strings.Index(got, "Done"), strings.Index(got, "Score")
	if vi < 0 || di < 0 || si < 0 {
		t.Fatalf("every declared field needs its label, got:\n%s", got)
	}
	// Declaration order, not map order — two runs of one pipeline must read the
	// same way round.
	if !(vi < di && di < si) {
		t.Errorf("fields must follow the declared order, got:\n%s", got)
	}
	if !strings.Contains(got, "yes") {
		t.Errorf("a bool should read as yes/no in a transcript, got:\n%s", got)
	}
	if !strings.Contains(got, "3") || strings.Contains(got, "3.0") {
		t.Errorf("a whole number should not render as a float, got:\n%s", got)
	}
}

// The rendering is display-only. Everything downstream — and the pipeline's
// return value — still sees the exact JSON the stage produced.
func TestTranscriptLeavesFreeTextAndTheDataPathAlone(t *testing.T) {
	plain := PipelineStage{Name: "synthesize", Kind: StageWorker}
	const answer = "Taqueria Vallarta, by a wide margin.\n\nSources: …"
	if got := transcriptBody(plain, answer, nil); got != answer {
		t.Errorf("a free-text stage must pass through untouched, got:\n%s", got)
	}
	// Declared stage, but nothing decoded: better the envelope than a blank card.
	declared := PipelineStage{Name: "d", Output: []PipelineField{{Name: "x", Type: FieldString}}}
	if got := transcriptBody(declared, `{"x":""}`, map[string]any{"x": ""}); got != `{"x":""}` {
		t.Errorf("an empty render must fall back to the raw output, got %q", got)
	}
	// The prompt-side renderer is a DIFFERENT function and must keep emitting
	// re-parseable JSON for lists — DecodeJSONList reads it downstream.
	if got := renderFieldValue([]any{"a", "b"}); got != `["a","b"]` {
		t.Errorf("the prompt-text renderer must stay compact JSON, got %q", got)
	}
}
