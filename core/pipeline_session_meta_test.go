package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// SessionMeta promotes declared stage output fields onto a run's sidebar row.
// Everything about it fails INVISIBLY when it is wrong — a bad reference does
// not error, it renders a blank pill — so the checks live at save time and
// these pin them.

func metaDef(refs ...string) PipelineDef {
	return PipelineDef{
		Name: "d",
		Stages: []PipelineStage{
			{Name: "judge", Kind: StageWorker, Prompt: "decide", Output: []PipelineField{
				{Name: "winner", Type: FieldString},
				{Name: "confidence", Type: FieldString},
			}},
			{Name: "prose", Kind: StageWorker, Prompt: "write"},
		},
		SessionMeta: refs,
	}
}

func TestSessionMetaAcceptsDeclaredFields(t *testing.T) {
	if err := metaDef("judge.winner", "judge.confidence").Validate(); err != nil {
		t.Fatalf("a reference to a declared field should validate: %v", err)
	}
}

func TestSessionMetaRefusesWhatWouldRenderBlank(t *testing.T) {
	cases := map[string]PipelineDef{
		"unknown stage":        metaDef("nope.winner"),
		"undeclared field":     metaDef("judge.loser"),
		"stage with no output": metaDef("prose.anything"),
		"not a reference":      metaDef("winner"),
		"duplicate name":       metaDef("judge.winner", "judge.winner"),
	}
	for name, def := range cases {
		if err := def.Validate(); err == nil {
			t.Errorf("%s: expected a refusal", name)
		}
	}
}

// A promoted field named for one of the row's own columns does not read as a
// bad name — it reads as a sidebar that lost its entries.
func TestSessionMetaRefusesTheRowsOwnColumns(t *testing.T) {
	for _, field := range []string{"id", "ID", "Title", "date"} {
		def := PipelineDef{
			Name: "d",
			Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x",
				Output: []PipelineField{{Name: field, Type: FieldString}}}},
			SessionMeta: []string{"s." + field},
		}
		if err := def.Validate(); err == nil {
			t.Errorf("promoting %q should be refused: it would overwrite the row's own column", field)
		}
	}
}

// The panel reads a promoted value as a key on the row OBJECT — the same place
// it reads ID and Title — so the row has to marshal flat, not nested.
func TestSessionRowMarshalsPromotedFieldsFlat(t *testing.T) {
	row := PipelineSessionRow{
		ID: "r1", Title: "Should X?", Date: time.Now(),
		Meta: map[string]string{"winner": "for", "confidence": "high"},
	}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	for k, want := range map[string]string{"winner": "for", "confidence": "high", "ID": "r1", "Title": "Should X?"} {
		if got[k] != want {
			t.Errorf("row[%q] = %v, want %q", k, got[k], want)
		}
	}
	if _, nested := got["Meta"]; nested {
		t.Error("promoted fields must be flat on the row, not nested under Meta")
	}
	if strings.Contains(string(b), `"meta"`) {
		t.Errorf("unexpected nested meta container: %s", b)
	}
}

// Validate refuses a def that promotes a reserved name, so a collision can
// only reach here from a run stored before a rename. Losing the row's id is a
// worse failure than losing a pill, so the column wins.
func TestSessionRowColumnsWinACollision(t *testing.T) {
	row := PipelineSessionRow{ID: "real", Title: "t", Meta: map[string]string{"ID": "hijacked"}}
	b, _ := json.Marshal(row)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["ID"] != "real" {
		t.Errorf("row ID = %v, want the row's own id", got["ID"])
	}
}

// The value has to travel from a finished stage to whoever is recording the
// run, and it goes through the SINK — the interpreter does not know whether
// anybody is storing this run and should not have to.
func TestPromoteSessionMetaEmitsOnlyWhatWasAskedFor(t *testing.T) {
	var got []PipelineEvent
	r := &pipelineRun{
		sink:        func(ev PipelineEvent) { got = append(got, ev) },
		sessionMeta: []string{"judge.winner", "judge.confidence", "other.thing"},
	}

	// A stage that declares none of them stays silent: no empty meta event.
	r.promoteSessionMeta("prose", map[string]any{"body": "words"})
	if len(got) != 0 {
		t.Fatalf("an unpromoted stage emitted %v", got)
	}

	r.promoteSessionMeta("judge", map[string]any{
		"winner": "for", "confidence": "high", "reasoning": "long prose nobody wants in a sidebar",
	})
	if len(got) != 1 || got[0].Kind != "meta" {
		t.Fatalf("expected one meta event, got %v", got)
	}
	if got[0].Meta["winner"] != "for" || got[0].Meta["confidence"] != "high" {
		t.Errorf("promoted values = %v", got[0].Meta)
	}
	// Only the named fields ride along. A stage's full output can be large,
	// and the sidebar is the one place it must not land.
	if _, carried := got[0].Meta["reasoning"]; carried {
		t.Error("an undeclared field was promoted")
	}
}

// A fanout branch is quiet — its per-stage blocks are suppressed so the
// transcript reads one entry per stage. A summary is not part of a transcript,
// so it is not suppressed with one.
func TestPromotedMetaIsNotSuppressedByQuiet(t *testing.T) {
	var got []PipelineEvent
	r := &pipelineRun{
		sink:        func(ev PipelineEvent) { got = append(got, ev) },
		sessionMeta: []string{"judge.winner"},
		quiet:       true,
	}
	r.promoteSessionMeta("judge", map[string]any{"winner": "against"})
	if len(got) != 1 {
		t.Fatalf("a quiet run still files its summary; got %v", got)
	}
}
