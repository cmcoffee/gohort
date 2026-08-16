package ui

// The repeating-rows field. Its contract is the JSON a form panel
// receives, so that is what this pins — a component whose wire shape
// drifts is a component the runtime silently stops rendering.

import (
	"encoding/json"
	"os"
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
	// one save.
	if !strings.Contains(src, "ctl.addEventListener('blur', persistRows)") {
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
	if !strings.Contains(src, "ctl.addEventListener('blur', persistRows);") {
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
