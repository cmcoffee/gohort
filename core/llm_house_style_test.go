package core

import (
	"strings"
	"testing"
)

// Tool descriptions are prompt text. Around 310 of them carried an em-dash,
// which put several hundred examples of the character in front of a model
// directly below a rule telling it never to produce one. A rule losing to its
// own prompt is not the model being stubborn.
func TestToolDescriptionsLoseEmDashesOnTheWayToTheModel(t *testing.T) {
	var cfg ChatConfig
	WithTools([]Tool{{
		Name:        "get_joke",
		Description: "Fetch a joke — one per call.",
		Parameters: map[string]ToolParam{
			"category": {Type: "string", Description: "Any category — or blank."},
			"opts": {Type: "object", Properties: map[string]ToolParam{
				"safe": {Type: "boolean", Description: "Family friendly — filters harshly."},
			}},
			"tags": {Type: "array", Items: &ToolParam{Type: "string", Description: "One tag — lowercase."}},
		},
	}})(&cfg)

	got := cfg.Tools[0]
	if strings.ContainsRune(got.Description, '—') {
		t.Errorf("tool description kept its em-dash: %q", got.Description)
	}
	if d := got.Parameters["category"].Description; strings.ContainsRune(d, '—') {
		t.Errorf("parameter kept its em-dash: %q", d)
	}
	// Nesting is where a partial pass would quietly miss: object properties and
	// array item schemas are descriptions too.
	if d := got.Parameters["opts"].Properties["safe"].Description; strings.ContainsRune(d, '—') {
		t.Errorf("nested object property kept its em-dash: %q", d)
	}
	if d := got.Parameters["tags"].Items.Description; strings.ContainsRune(d, '—') {
		t.Errorf("array item schema kept its em-dash: %q", d)
	}
}

// Names and enum values are identifiers the model has to reproduce EXACTLY.
// Rewriting one would break the call it is meant to make, so the pass touches
// descriptions and nothing else.
func TestToolIdentifiersAreNeverRewritten(t *testing.T) {
	var cfg ChatConfig
	WithTools([]Tool{{
		Name:        "weird—name",
		Description: "x",
		Parameters:  map[string]ToolParam{"mode": {Type: "string", Enum: []string{"a—b", "c"}}},
	}})(&cfg)

	if cfg.Tools[0].Name != "weird—name" {
		t.Errorf("tool NAME was rewritten to %q; the model could no longer call it", cfg.Tools[0].Name)
	}
	if got := cfg.Tools[0].Parameters["mode"].Enum[0]; got != "a—b" {
		t.Errorf("enum VALUE was rewritten to %q; it would no longer match", got)
	}
}

// The caller's slice is theirs. Rewriting in place would mutate a tool catalog
// that other calls share.
func TestWithToolsDoesNotMutateTheCallersTools(t *testing.T) {
	orig := []Tool{{Name: "t", Description: "keep — this"}}
	var cfg ChatConfig
	WithTools(orig)(&cfg)
	if orig[0].Description != "keep — this" {
		t.Errorf("caller's tool was mutated: %q", orig[0].Description)
	}
}
