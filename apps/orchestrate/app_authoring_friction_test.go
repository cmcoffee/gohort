package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// Three ways the authoring surface used to mislead an author into rebuilding an
// app that was never broken. Each one on its own costs a round; together they
// cost a working app, replaced by a hand-written one.

// A key the framework drops must SAY it dropped it. Silence reads as "accepted",
// so the author looks for the failure somewhere real — and concludes the section
// kind itself is broken.
func TestUnknownSectionKeysAreReported(t *testing.T) {
	notes := unknownSectionKeyNotes([]any{
		map[string]any{
			"kind":           "pipeline",
			"pipeline_label": "Deep Dive Research", // invented
			"source_script":  "deep_dive_run",      // borrowed from table/display
			"submit_label":   "Research",           // real
		},
	})
	if len(notes) != 1 {
		t.Fatalf("want one note, got %v", notes)
	}
	for _, want := range []string{"section 1", "pipeline_label", "source_script"} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("note should name %q, got %q", want, notes[0])
		}
	}
	if strings.Contains(notes[0], "submit_label —") {
		t.Errorf("a key the kind DOES read must not be reported: %q", notes[0])
	}
	// Every key recognized → nothing to say. A note on a clean section would
	// train the reader to skip notes, which is worse than not having them.
	if n := unknownSectionKeyNotes([]any{
		map[string]any{"kind": "table", "columns": []any{}, "empty_text": "none", "deletable": true, "title": "Rows"},
	}); len(n) != 0 {
		t.Errorf("a section using only real keys must produce no note, got %v", n)
	}
	// An unknown KIND is the section builder's error to raise, with its own
	// message listing the kinds. Reporting every key on it as unknown would
	// bury that.
	if n := unknownSectionKeyNotes([]any{map[string]any{"kind": "not_a_kind", "whatever": 1}}); len(n) != 0 {
		t.Errorf("an unknown kind is not this check's business, got %v", n)
	}
}

// pipeline_id on the SECTION is where an author naturally writes it — that is
// where the binding is used. Honor it instead of saving an app with a pipeline
// section and no pipeline.
func TestSectionLevelPipelineIDIsHonored(t *testing.T) {
	got := sectionPipelineRef([]any{
		map[string]any{"kind": "display", "pairs": []any{}},
		map[string]any{"kind": "pipeline", "pipeline_id": "f827e5d5"},
	})
	if got != "f827e5d5" {
		t.Errorf("section-level pipeline_id = %q, want it honored", got)
	}
	if got := sectionPipelineRef([]any{map[string]any{"kind": "pipeline"}}); got != "" {
		t.Errorf("no binding anywhere must stay empty, got %q", got)
	}
	if got := sectionPipelineRef("not an array"); got != "" {
		t.Errorf("garbage in, empty out, got %q", got)
	}
}

// The render check and the runtime have to agree on how a mounted section is
// recognized. They didn't: a no-chrome section (chat, workbench, pipeline)
// mounts its body with no .ui-section card, so a page built only of them
// counted ZERO sections and verify called a working app blank — which is a
// verdict an author acts on by rebuilding.
func TestNoChromeSectionsStayCountableByVerify(t *testing.T) {
	epilogue, err := os.ReadFile("../../core/ui/assets/runtime/99_epilogue.js")
	if err != nil {
		t.Fatalf("read runtime epilogue: %v", err)
	}
	const marker = "data-ui-section"
	if !strings.Contains(string(epilogue), marker) {
		t.Fatalf("the runtime no longer marks mounted sections with %q — verify's count will read a live panel as a blank page", marker)
	}
	src, err := os.ReadFile("app_def_tool.go")
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if !strings.Contains(string(src), "[data-ui-section]") {
		t.Fatalf("verify's probe must count %q too, or it reports a page of no-chrome sections as blank", marker)
	}
}

// "a form section needs at least one field" was true of the PARSED result and
// false of the payload: three fields arrived and all three were discarded for
// carrying the wrong key. Reading a message that contradicted what it had just
// sent, the author re-sent the same shape six times, then simplified to one
// field to isolate it — and got the same sentence, because the count was never
// the problem. Twice in one session: once for fields, once for columns.
func TestEmptyFieldListSaysWhatWasDropped(t *testing.T) {
	err := entryListError("form", "field", "fields", []any{
		map[string]any{"name": "proposition", "label": "Proposition", "type": "textarea"},
		map[string]any{"name": "side_a", "label": "Side A", "type": "text"},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "2") || !strings.Contains(msg, "DROPPED") {
		t.Errorf("say how many arrived and that they were discarded, got: %s", msg)
	}
	if !strings.Contains(msg, "label") || !strings.Contains(msg, "type") {
		t.Errorf("name the keys the entries DO carry — that is the route to the fix, got: %s", msg)
	}
	// Genuinely empty still reads as the simple thing it is.
	if msg := entryListError("form", "field", "fields", []any{}).Error(); strings.Contains(msg, "DROPPED") {
		t.Errorf("an empty list was not dropped, it was empty: %s", msg)
	}
	// Wrong type entirely — say so rather than counting to zero.
	if msg := entryListError("table", "column", "columns", "proposition").Error(); !strings.Contains(msg, "ARRAY") {
		t.Errorf("a non-array should be named as such, got: %s", msg)
	}
	// The alias hint fires on the near-misses an author actually writes.
	if msg := entryListError("table", "column", "columns", []any{
		map[string]any{"key": "id", "label": "#"},
	}).Error(); !strings.Contains(msg, `Rename "key"`) {
		t.Errorf("suggest the rename when a near-miss key is present, got: %s", msg)
	}
}

// One spelling, one meaning, in every section. `name` worked in a pipeline
// section and failed in a form and a table, which is how the same payload
// produced a working section and two dead ones in a single call.
func TestFieldAndNameAreTheSameKeyEverywhere(t *testing.T) {
	ff := appFormFields([]any{map[string]any{"name": "proposition", "label": "Proposition", "type": "textarea"}})
	if len(ff) != 1 || ff[0].Field != "proposition" {
		t.Errorf("a form field keyed `name` must parse, got %+v", ff)
	}
	cols := appTableCols([]any{map[string]any{"name": "winner", "label": "Winner"}})
	if len(cols) != 1 || cols[0].Field != "winner" {
		t.Errorf("a table column keyed `name` must parse, got %+v", cols)
	}
	// `field` still wins when both are present — it is the canonical spelling.
	both := appFormFields([]any{map[string]any{"field": "real", "name": "other"}})
	if len(both) != 1 || both[0].Field != "real" {
		t.Errorf("field must take precedence over name, got %+v", both)
	}
}

// A page can parse perfectly and still promise something it cannot do. Both of
// these shipped: a table of "past debates" that nothing ever writes, and two
// extra submit fields templated into prompts that never receive them.
func TestShapeNotesCatchThePromisesAPageCannotKeep(t *testing.T) {
	notes := appShapeNotes([]any{
		map[string]any{"kind": "pipeline", "fields": []any{
			map[string]any{"name": "proposition", "type": "textarea"},
			map[string]any{"name": "side_a", "type": "text"},
			map[string]any{"name": "side_b", "type": "text"},
		}},
		map[string]any{"kind": "table", "columns": []any{map[string]any{"field": "winner"}}},
	}, true)
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "side_a") || !strings.Contains(joined, "side_b") {
		t.Errorf("submit fields the run never reads must be named: %s", joined)
	}
	if strings.Contains(joined, "proposition") {
		t.Errorf("a field named proposition is stray too, but the note should not claim the INPUT field is: %s", joined)
	}
	if !strings.Contains(joined, "RECORD store") {
		t.Errorf("a record-backed table beside a pipeline never fills: %s", joined)
	}
	// A computed table is fine beside a pipeline — it fills itself.
	quiet := appShapeNotes([]any{
		map[string]any{"kind": "pipeline", "fields": []any{map[string]any{"name": "topic", "type": "textarea"}}},
		map[string]any{"kind": "table", "source_script": "summary", "columns": []any{map[string]any{"field": "x"}}},
	}, true)
	if len(quiet) != 0 {
		t.Errorf("a clean shape must produce no notes, got %v", quiet)
	}
}

// Naming what a kind READS answers "why was this dropped" but not "where does
// it go". The gap cost three rounds: an actions array nested in an actions
// SECTION was re-sent unchanged, then moved on a guess.
func TestMisplacedTopLevelKeysSayWhereTheyBelong(t *testing.T) {
	notes := unknownSectionKeyNotes([]any{
		map[string]any{"kind": "actions", "actions": []any{map[string]any{"name": "save"}}},
	})
	if len(notes) != 1 {
		t.Fatalf("want one note, got %v", notes)
	}
	if !strings.Contains(notes[0], "TOP-LEVEL") || !strings.Contains(notes[0], `beside "sections"`) {
		t.Errorf("the note must say where the key belongs, got: %s", notes[0])
	}
	// A key that is simply invented gets no relocation advice — there is
	// nowhere to move it to, and a wrong hint is worse than none.
	made := unknownSectionKeyNotes([]any{map[string]any{"kind": "pipeline", "pipeline_label": "x"}})
	if strings.Contains(strings.Join(made, ""), "TOP-LEVEL") {
		t.Errorf("an invented key has no home to point at: %v", made)
	}
	// pipeline_id is valid in BOTH places, so it is never reported at all.
	if n := unknownSectionKeyNotes([]any{map[string]any{"kind": "pipeline", "pipeline_id": "p1"}}); len(n) != 0 {
		t.Errorf("pipeline_id is accepted on the section; it must not be flagged: %v", n)
	}
}

// The end state of the worst run: an app that binds a pipeline, grows a form, a
// table and a script-backed "run" button, and has no way to start the thing it
// is for. Everything parsed, verify passed, and the promised behavior did not
// exist anywhere on the page.
func TestBoundPipelineWithNoPipelineSectionIsReported(t *testing.T) {
	notes := appShapeNotes([]any{
		map[string]any{"kind": "form", "fields": []any{map[string]any{"field": "draft"}}},
		map[string]any{"kind": "table", "columns": []any{map[string]any{"field": "id"}}},
	}, true)
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "NO section of kind") {
		t.Errorf("a bound pipeline with no section to run it must be reported: %v", notes)
	}
	if !strings.Contains(joined, "action script cannot run a pipeline") {
		t.Errorf("say why the obvious workaround is not one: %v", notes)
	}
	// No binding, no complaint — that app is simply not a pipeline app.
	if n := appShapeNotes([]any{
		map[string]any{"kind": "form", "fields": []any{map[string]any{"field": "draft"}}},
	}, false); len(n) != 0 {
		t.Errorf("an app with no pipeline_id has nothing missing: %v", n)
	}
}

// An app built to run a five-pass pipeline had a working pipeline section,
// verified. The next update dropped it while the author narrated adding live
// progress, and everything downstream passed — a form and a table render fine.
// What shipped was a form, a table, and a button that set a status field.
func TestUpdateRefusesToSilentlyDropAFunctionalSection(t *testing.T) {
	prior := []map[string]any{
		{"kind": "form", "fields": []any{map[string]any{"field": "draft"}}},
		{"kind": "pipeline"},
		{"kind": "table", "columns": []any{map[string]any{"field": "id"}}},
	}
	next := []map[string]any{
		{"kind": "form", "fields": []any{map[string]any{"field": "draft"}}},
		{"kind": "table", "columns": []any{map[string]any{"field": "id"}}},
	}
	risk := appDroppedFunctionSection(prior, next)
	if risk == "" {
		t.Fatal("dropping the section that runs the pipeline must not pass silently")
	}
	for _, want := range []string{"pipeline", "REPLACES the sections array", "confirm_rewrite"} {
		if !strings.Contains(risk, want) {
			t.Errorf("the refusal must contain %q so it is actionable, got:\n%s", want, risk)
		}
	}
	// Ordinary edits are not refusals: adding, reordering and re-titling all
	// keep the functional section, and a guard that fires on those gets muted.
	if r := appDroppedFunctionSection(prior, []map[string]any{
		{"kind": "pipeline", "title": "Renamed"},
		{"kind": "form", "fields": []any{map[string]any{"field": "draft"}}},
		{"kind": "table", "columns": []any{map[string]any{"field": "id"}}},
		{"kind": "display"},
	}); r != "" {
		t.Errorf("reordering, re-titling and adding are ordinary edits: %s", r)
	}
	// A page with nothing load-bearing has nothing to lose.
	if r := appDroppedFunctionSection([]map[string]any{{"kind": "form"}}, nil); r != "" {
		t.Errorf("no functional section to drop: %s", r)
	}
	// A spec stored before authoring-sections existed gives the guard nothing
	// to compare — staying quiet is the right way to be wrong.
	if r := appDroppedFunctionSection(nil, next); r != "" {
		t.Errorf("no prior sections recorded means no verdict: %s", r)
	}
	// chat and workbench are load-bearing for the same reason.
	if r := appDroppedFunctionSection([]map[string]any{{"kind": "workbench"}}, []map[string]any{{"kind": "table"}}); !strings.Contains(r, "workbench") {
		t.Errorf("a workbench IS the app; dropping it must be refused: %s", r)
	}
}
