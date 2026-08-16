package ui

// The repeating-rows field. Its contract is the JSON a form panel
// receives, so that is what this pins — a component whose wire shape
// drifts is a component the runtime silently stops rendering.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRowsFieldMarshalsItsColumns(t *testing.T) {
	f := FormField{
		Field: "output", Type: "rows", Label: "What this step hands on",
		AddLabel: "+ Add output",
		Columns: []FormField{
			{Field: "name", Label: "Field", Type: "text", Width: 2},
			{Field: "type", Label: "Type", Type: "select", Options: []SelectOption{
				{Value: "string", Label: "text"},
				{Value: "list", Label: "list"},
			}},
			{Field: "required", Label: "Required", Type: "toggle"},
		},
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`"type":"rows"`,
		`"columns":[`,
		`"add_label":"+ Add output"`,
		`"width":2`,
		`"field":"required"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s from the wire form:\n%s", want, body)
		}
	}
	// A column is a FormField, so it carries the same vocabulary — a
	// select column keeps its options rather than needing a parallel type.
	if !strings.Contains(body, `"options":[{"value":"string"`) {
		t.Errorf("column options were dropped:\n%s", body)
	}
}

// Empty extras stay absent: these fields ride on EVERY form field, and a
// form with thirty fields should not carry thirty empty columns arrays.
func TestRowsExtrasAreOmittedWhenUnused(t *testing.T) {
	raw, _ := json.Marshal(FormField{Field: "name", Type: "text"})
	for _, unwanted := range []string{"columns", "add_label", "width"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("%q should be omitted on a plain field: %s", unwanted, raw)
		}
	}
}

// The runtime half is in a file that knows nothing about this one.
func TestRowsFieldIsRenderedByTheRuntime(t *testing.T) {
	raw, err := os.ReadFile("assets/runtime/10_basics.js")
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "t === 'rows'") {
		t.Fatal("the runtime never renders a rows field — the Go side would describe a control that does not exist")
	}
	// Adding a blank row must not save: an empty row is not data, and
	// persisting it makes validation complain about a field nobody has
	// reached yet.
	if !strings.Contains(src, "// No save here: an empty row is not data yet") {
		t.Error("adding a row should not persist until it has content")
	}
	// Text cells commit on blur, not per keystroke — the whole array is
	// one save. (Through the row's commit gate, which also redraws when
	// the change moved the row's shape; see the gate test below.)
	if !strings.Contains(src, "ctl.addEventListener('blur', commit)") {
		t.Error("a text cell should commit on blur rather than per character")
	}
}

// A cell that holds an INSTRUCTION cannot be a single-line input. The
// box shapes what gets written in it: a one-line cell teaches people to
// write three words, which is fine for a label and useless when the
// value is the directive the model acts on.
func TestRowsSupportsATextareaColumn(t *testing.T) {
	f := FormField{
		Field: "output", Type: "rows",
		Columns: []FormField{
			{Field: "name", Type: "text", Label: "Field"},
			{Field: "desc", Type: "textarea", Rows: 3, Width: 6, Label: "What to work out"},
		},
	}
	raw, _ := json.Marshal(f)
	if !strings.Contains(string(raw), `"type":"textarea"`) {
		t.Errorf("a textarea column should survive to the wire:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"rows":3`) {
		t.Errorf("its height should too:\n%s", raw)
	}

	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "c.type === 'textarea'") {
		t.Fatal("the runtime does not render a textarea cell — the Go side would describe a control that does not exist")
	}
	// It grows with its content: a long instruction inside a scrollbar in
	// a table row is unreadable, which defeats the point of allowing one.
	if !strings.Contains(src, "ctl.scrollHeight") {
		t.Error("a textarea cell should grow with what is typed into it")
	}
	// And commits on blur like every other cell — the whole array is one
	// save.
	if !strings.Contains(src, "ctl.addEventListener('blur', commit);") {
		t.Error("a textarea cell should commit on blur")
	}
}

func readRuntimeFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("assets/runtime/" + name)
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	return string(raw)
}

// A combo cell is a text box with a wheel: pick one of the caller's
// values or type your own. It exists because a "select" fences the set
// and a bare "text" hides it, and the common case is neither — a name
// that is USUALLY one of a known few.
func TestRowsSupportsAComboColumn(t *testing.T) {
	f := FormField{
		Field: "output", Type: "rows",
		Columns: []FormField{
			{Field: "name", Type: "combo", Label: "Field",
				Options: []SelectOption{{Value: "now", Label: "the date and time"}}},
		},
	}
	raw, _ := json.Marshal(f)
	if !strings.Contains(string(raw), `"type":"combo"`) {
		t.Errorf("a combo column should survive to the wire:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"value":"now"`) {
		t.Errorf("its suggestions should too:\n%s", raw)
	}

	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "c.type === 'combo'") {
		t.Fatal("the runtime does not render a combo cell — the Go side would describe a control that does not exist")
	}
	// Native datalist, so there is no popup of ours to get wrong: no
	// outside-click handling, no keyboard navigation, no z-index.
	for _, want := range []string{"datalist", "list: listId"} {
		if !strings.Contains(src, want) {
			t.Errorf("combo should be a native datalist; missing %q", want)
		}
	}
	// Ids are document-global — two rows sharing one would put the first
	// row's suggestions on every later cell.
	if !strings.Contains(src, "++comboSeq") {
		t.Error("each combo cell needs its own datalist id")
	}
}

// Row-scoped conditions. A row that has settled into a kind where the
// other columns do not apply should not keep offering them: a control
// somebody can change and be ignored for is worse than one that is not
// there.
func TestRowsColumnsCanHideAndLockPerRow(t *testing.T) {
	f := FormField{
		Field: "output", Type: "rows",
		Columns: []FormField{
			{Field: "name", Type: "combo", LockWhen: "name:now|user"},
			{Field: "type", Type: "select", HideWhen: "name:now|user"},
		},
	}
	raw, _ := json.Marshal(f)
	for _, want := range []string{`"lock_when":"name:now|user"`, `"hide_when":"name:now|user"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the condition should survive to the wire (%s):\n%s", want, raw)
		}
	}

	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "c.hide_when && matchesWhen(c.hide_when, row)") {
		t.Error("hide_when is not evaluated against the ROW")
	}
	if !strings.Contains(src, "c.lock_when && matchesWhen(c.lock_when, row)") {
		t.Error("lock_when is not evaluated against the ROW")
	}
	// Same grammar as the form's own show_when, or an author has two
	// things to learn for one idea.
	if !strings.Contains(src, "function matchesShowWhen(expr) { return matchesWhen(expr, current); }") {
		t.Error("the row condition should reuse the show_when evaluator, not a second one")
	}
	// A row that changes kind has to redraw, or it keeps the controls it
	// just stopped having — and only then, so committing a cell that
	// changed nothing structural does not yank focus.
	if !strings.Contains(src, "if (rowShape(row) !== shapeAtDraw) drawRows();") {
		t.Error("changing a cell should redraw the row when it moves the row's shape")
	}
}

// The checklist had two implementations in one if-chain: a flat one that
// always won, and a grouped one (headers, select-all, count) that was
// unreachable from the day it was written — its CSS shipped, its code
// never ran. This pins the survivor, and the filter that makes a long
// list (a tool pool is ~100 entries) scannable.
func TestChecklistIsTheGroupedRendererWithAFilter(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")

	if n := strings.Count(src, "else if (t === 'checklist')"); n != 1 {
		t.Fatalf("expected exactly one checklist branch, found %d — a second one shadows the first", n)
	}
	for _, want := range []string{"ui-checklist-group", "ui-checklist-toolbar", "ui-checklist-count"} {
		if !strings.Contains(src, want) {
			t.Errorf("the grouped renderer should be the live one; missing %q", want)
		}
	}
	// The filter appears once the list is too long to scan, matches
	// name/label/help, and hides headers left with nothing under them.
	for _, want := range []string{"ui-checklist-filter", ".length > 15", "e.text.indexOf(q)"} {
		if !strings.Contains(src, want) {
			t.Errorf("long checklists need the filter; missing %q", want)
		}
	}
	// Select all / Clear act on VISIBLE rows, so a filtered "Select all"
	// cannot check a hundred rows the person never saw.
	if !strings.Contains(src, "function eachVisible") {
		t.Error("select-all/clear should act on the filtered view, not the whole set")
	}
}

// ReloadOnChange has two halves and they are one decision: the save
// reloads the page, so the save must not fire mid-typing. A debounced
// identity field would rename the record to half a word and reload the
// page under the person typing the rest.
func TestReloadOnChangeFieldsCommitOnBlur(t *testing.T) {
	f := FormField{Field: "name", Type: "text", ReloadOnChange: true}
	raw, _ := json.Marshal(f)
	if !strings.Contains(string(raw), `"reload_on_change":true`) {
		t.Fatalf("the flag should reach the runtime: %s", raw)
	}

	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "if (!f.reload_on_change) {\n          input.addEventListener('input', function(){ debounced(f.field, input.value); });") {
		t.Error("a reload_on_change text field must skip the typing debounce and commit on blur")
	}
	if !strings.Contains(src, "if (fdef && fdef.reload_on_change) window.location.reload()") {
		t.Error("a successful save of such a field should reload the page")
	}
}

// The half of row conditions that is easy to ship broken: a cell that
// CHANGES a condition has to redraw the row. A select whose change
// handler only saved left the row showing the controls its new kind
// does not have — the built-in field's name box, type and instruction
// all still on screen, and the kind still editable, until a page
// reload. Every cell commits through one closure now.
func TestEveryRowCellCommitsThroughTheSameGate(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")

	// The gate exists and redraws only when the row's SHAPE moved —
	// redrawing on every commit would yank the control just tabbed into.
	if !strings.Contains(src, "function rowShape(row)") {
		t.Fatal("no way to tell a shape change from a value change")
	}
	if !strings.Contains(src, "if (rowShape(row) !== shapeAtDraw) drawRows();") {
		t.Error("the redraw should be conditional on the row's shape changing")
	}
	// No cell handler may save directly: persistRows is the low-level
	// write, reachable from the commit gate and the row add/remove/move
	// controls (which redraw themselves) — nowhere else.
	if n := strings.Count(src, "persistRows()"); n > 4 {
		t.Errorf("a cell handler is saving without the commit gate (%d direct calls)", n)
	}
	for _, branch := range []string{
		"row[c.field] = ctl.value;\n                  commit();",   // select
		"row[c.field] = ctl.checked;\n                  commit();", // toggle
		"ctl.addEventListener('blur', commit);",                    // text + textarea
	} {
		if !strings.Contains(src, branch) {
			t.Errorf("a cell type still commits its own way: %q", branch)
		}
	}
}

// A repeating row of bare controls is a puzzle: a select, a text box
// and a checkbox side by side say nothing about which is which, and the
// title attribute each carries is a tooltip, not a label. And a cell
// holding an INSTRUCTION cannot share a line with the short cells that
// identify the row — either it is too narrow to write in or they are
// too narrow to read.
func TestRowsHaveHeadersAndCanGiveALineToOneCell(t *testing.T) {
	f := FormField{
		Field: "output", Type: "rows",
		Columns: []FormField{
			{Field: "name", Type: "text", Label: "Name", Width: 3},
			{Field: "desc", Type: "textarea", Label: "What to work out", OwnLine: true},
		},
	}
	raw, _ := json.Marshal(f)
	if !strings.Contains(string(raw), `"own_line":true`) {
		t.Errorf("the flag should reach the runtime:\n%s", raw)
	}

	src := readRuntimeFile(t, "10_basics.js")
	// Headers, over the top-line columns only — an own_line cell labels
	// itself, above its own control.
	if !strings.Contains(src, "ui-rows-head") {
		t.Error("rows should be headed by their column labels")
	}
	if !strings.Contains(src, "if (c.label && !c.own_line)") &&
		!strings.Contains(src, "c.label && !c.own_line") {
		t.Error("the header should cover exactly the cells that share the top line")
	}
	if !strings.Contains(src, "ui-rows-wide-label") {
		t.Error("an own_line cell should carry its own label")
	}
	// Alignment under those headers survives a hidden cell: a hidden
	// top-line cell holds its column open, a hidden own_line cell does
	// not leave an empty band.
	if !strings.Contains(src, "if (!c.own_line) {") {
		t.Error("a hidden cell should only hold a column open when it HAS one")
	}
	// And a multi-line row has to read as one row.
	if !strings.Contains(src, "ui-rows-row") {
		t.Error("a row that spans lines needs to be bounded, or it blurs into the next")
	}
	css := readRuntimeCSSForTest(t)
	for _, want := range []string{".ui-rows-head", ".ui-rows-row", ".ui-rows-wide-label"} {
		if !strings.Contains(css, want) {
			t.Errorf("%s has no styling, so it renders as undifferentiated text", want)
		}
	}
}

func readRuntimeCSSForTest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("assets", "runtime.css"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Two ways a rows editor comes out crooked, both geometry rather than
// wiring, and both invisible to a test that only reads the spec.
func TestRowsGeometryLinesUp(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	css := readRuntimeCSSForTest(t)

	// 1. The header must reserve the width of the row's own controls.
	// Without it every column sits narrower than its label by a share of
	// that width — an error that accumulates left to right, so the
	// headings drift further off the further right you read.
	if !strings.Contains(src, "ui-rows-head-spacer") {
		t.Error("the header does not reserve room for the per-row controls, so it cannot line up")
	}
	if !strings.Contains(src, `el('div', {class: 'ui-rows-actions'}`) {
		t.Error("the controls cell should take its width from the same variable the header does")
	}
	if !strings.Contains(css, "--rows-actions:") {
		t.Error("the two widths should come from ONE definition, or they drift apart")
	}
	for _, sel := range []string{".ui-rows-actions", ".ui-rows-head-spacer"} {
		if !strings.Contains(css, sel+" { flex: 0 0 var(--rows-actions)") &&
			!strings.Contains(css, "flex: 0 0 var(--rows-actions);") {
			t.Errorf("%s should be pinned to the shared width", sel)
		}
	}

	// 2. A control given its own line has to fill it. An input keeps its
	// intrinsic width in a plain block, so the instruction box otherwise
	// sits in a narrow column on the left of the space it was given.
	if !strings.Contains(css, ".ui-rows-wide > textarea") || !strings.Contains(css, "width: 100%") {
		t.Error("an own_line control should fill its line")
	}
}

// What "the field is filled in" means to a show_when. Plain truthiness
// gets one case badly wrong: an empty ARRAY is truthy in JavaScript, so
// a checklist with nothing checked read as present and "!field" never
// fired for one — which is the whole mechanism for "hide this while the
// other way of doing it is in use".
func TestShowWhenTreatsAnEmptyListAsUnanswered(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	if !strings.Contains(src, "function hasValue(v)") {
		t.Fatal("show_when should ask one question about presence, in one place")
	}
	if !strings.Contains(src, "if (Array.isArray(v)) return v.length > 0;") {
		t.Error("an empty checklist should read as unanswered, like an empty string")
	}
	// Both forms of the presence test go through it, or "field" and
	// "!field" disagree about the same value.
	for _, form := range []string{
		"if (hasValue(current[c.substring(1).trim()])) return false;",
		"if (!hasValue(current[c])) return false;",
	} {
		if !strings.Contains(src, form) {
			t.Errorf("presence check bypassed: %q", form)
		}
	}
}

// A SectionNav rail can nest. Without it every rail is a flat list, and
// two sections that are ALTERNATIVES look exactly like two that follow
// one another — the distinction a rail over a branching thing most
// needs to make.
func TestSectionsCanNestInTheRail(t *testing.T) {
	// The wire shape is what the PAGE writes, not what a Section
	// marshals to on its own — that is the copy the runtime reads.
	w := httptest.NewRecorder()
	Page{Title: "m", SectionNav: true, Sections: []Section{
		{Title: "triage"},
		{Title: "dig", Indent: 1},
	}}.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"indent":1`) {
		t.Error("the nesting should reach the runtime")
	}
	// A flat section must not carry the key at all — every existing rail
	// in the product renders exactly as before.
	if strings.Count(body, `"indent"`) != 1 {
		t.Errorf("only the nested section should carry it, found %d", strings.Count(body, `"indent"`))
	}

	src := readRuntimeFile(t, "99_epilogue.js")
	if !strings.Contains(src, "if (s.indent > 0)") {
		t.Error("the rail does not read the nesting")
	}
	if !strings.Contains(src, "classList.add('nested')") {
		t.Error("nesting needs a class of its own — indentation alone reads as a typo")
	}
	if css := readRuntimeCSSForTest(t); !strings.Contains(css, ".ui-secnav-item.nested") {
		t.Error("nested rail entries have no styling to distinguish them")
	}
}
