package core

import (
	"encoding/json"
	"os"
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

// A pipeline backing an app takes the submit form's fields as {name} in a
// stage prompt — the tool's help says so without qualification. It was true of
// a worker stage and quietly false of a fanout and a panel, which re-derive
// the prompt from stage.Prompt and used to stop at resolveStageTemplate. The
// failure is silent and it lands in the two kinds a debate-shaped app leans on
// hardest: the placeholder reaches the model as literal text.
func TestFormValuesReachEveryStageKindsPrompt(t *testing.T) {
	r := &pipelineRun{
		input:   "the question",
		vars:    map[string]string{"{tone}": "harsh"},
		outputs: map[string]stageOutput{},
	}
	const tmpl = "argue in a {tone} register"
	const want = "argue in a harsh register"

	// The worker path, which always worked, as the reference.
	if got := r.applyRunVars(resolveStageTemplate(tmpl, r.input, "", r.outputs)); got != want {
		t.Fatalf("worker prompt = %q, want %q", got, want)
	}
	// The two that did not: same composition, checked at the source so a
	// future edit that drops one is caught rather than shipped.
	src, err := os.ReadFile("pipeline_interp.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{
		`r.applyRunVars(resolveStageTemplate(strings.ReplaceAll(stage.Prompt, "{item}", it)`,
		`r.applyRunVars(resolveStageTemplate(panelPrompt(stage.Prompt`,
	} {
		if !strings.Contains(string(src), site) {
			t.Errorf("a stage kind stopped applying the run's form values: %s", site)
		}
	}
}

// The number of rounds is the one thing about a debate that belongs to the
// QUESTION rather than to the recipe, and a definition is written once for
// every question it will ever run. count_from moves it to the run.
func TestCountFromTakesTheCountFromTheRun(t *testing.T) {
	var said []string
	status := func(s string) { said = append(said, s) }
	run := func(vars map[string]string, stage PipelineStage) int {
		r := &pipelineRun{input: "q", vars: vars, outputs: map[string]stageOutput{}}
		return r.resolveCount(stage, 8, status)
	}
	stage := PipelineStage{Name: "rounds", Count: 3, CountFrom: "{rounds}"}

	if got := run(map[string]string{"{rounds}": "5"}, stage); got != 5 {
		t.Errorf("a form value of 5 gave %d rounds", got)
	}
	// An empty optional field is not a mistake, so it falls back QUIETLY.
	before := len(said)
	if got := run(nil, stage); got != 3 {
		t.Errorf("an unfilled field gave %d rounds, want the fallback", got)
	}
	if len(said) != before {
		t.Errorf("falling back on an unfilled field should say nothing, said: %v", said[before:])
	}
	// Anything else falls back LOUDLY: a stage that ran a different number of
	// times than the submitter asked for is not visible from the result.
	before = len(said)
	if got := run(map[string]string{"{rounds}": "lots"}, stage); got != 3 {
		t.Errorf("a non-number gave %d rounds, want the fallback", got)
	}
	if len(said) == before {
		t.Error("a value that is not a count must be reported, not silently ignored")
	}
	// The ceiling is the ceiling, and it says so too.
	before = len(said)
	if got := run(map[string]string{"{rounds}": "99"}, stage); got != 8 {
		t.Errorf("99 rounds gave %d, want the ceiling", got)
	}
	if len(said) == before {
		t.Error("clamping to the ceiling must be reported")
	}
}

// An earlier stage deciding how many rounds the question warrants is the
// declarative form of debate's "auto".
func TestCountFromCanReadAnEarlierStage(t *testing.T) {
	r := &pipelineRun{
		input: "q",
		outputs: map[string]stageOutput{
			"plan": {Fields: map[string]any{"rounds": float64(4)}},
		},
	}
	got := r.resolveCount(PipelineStage{Name: "rounds", Count: 2, CountFrom: "{stage:plan.rounds}"}, 8, nil)
	if got != 4 {
		t.Errorf("count from an earlier stage = %d, want 4", got)
	}
}

// A field that exists on the shared stage struct but is read by only some
// kinds is the lying-control pattern this codebase keeps refusing.
func TestCountFromIsRefusedWhereNothingRepeats(t *testing.T) {
	def := PipelineDef{Name: "d", Stages: []PipelineStage{
		{Name: "s", Kind: StageWorker, Prompt: "x", CountFrom: "{rounds}"},
	}}
	if err := def.Validate(); err == nil {
		t.Error("count_from on a worker stage should be refused — nothing there repeats")
	}
}
