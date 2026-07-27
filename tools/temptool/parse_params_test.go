package temptool

import (
	"strings"
	"testing"
)

// TestParseParamsArgCoerces pins the fix for the tool_def grind loop: a
// wrong-but-close param shape (bare bool / bare type / bare description /
// synonym type) is COERCED into a valid ToolParam instead of hard-rejected,
// so authoring converges instead of retrying forever.
func TestParseParamsArgCoerces(t *testing.T) {
	cases := []struct {
		name     string
		in       any
		wantType map[string]string // param -> expected type
		wantDesc map[string]string // param -> expected description (optional)
		wantErr  bool
	}{
		{
			name:     "bare bool value (the observed failure)",
			in:       map[string]any{"sid": true, "serveruid": false},
			wantType: map[string]string{"sid": "string", "serveruid": "string"},
		},
		{
			name:     "bare type string",
			in:       map[string]any{"count": "integer", "name": "string"},
			wantType: map[string]string{"count": "integer", "name": "string"},
		},
		{
			name:     "type synonyms",
			in:       map[string]any{"a": "int", "b": "bool", "c": "float", "d": "str"},
			wantType: map[string]string{"a": "integer", "b": "boolean", "c": "number", "d": "string"},
		},
		{
			name:     "bare description string",
			in:       map[string]any{"sid": "the server id"},
			wantType: map[string]string{"sid": "string"},
			wantDesc: map[string]string{"sid": "the server id"},
		},
		{
			name:     "correct object shape passes through",
			in:       map[string]any{"q": map[string]any{"type": "string", "description": "query"}},
			wantType: map[string]string{"q": "string"},
			wantDesc: map[string]string{"q": "query"},
		},
		{
			name:     "object with missing type defaults to string",
			in:       map[string]any{"q": map[string]any{"description": "query"}},
			wantType: map[string]string{"q": "string"},
			wantDesc: map[string]string{"q": "query"},
		},
		{
			name:     "nil params is a valid empty set",
			in:       nil,
			wantType: map[string]string{},
		},
		{
			name:    "bad param name still rejected",
			in:      map[string]any{"Bad Name": "string"},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseParamsArg(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for k, wt := range c.wantType {
				if got[k].Type != wt {
					t.Errorf("param %q type = %q, want %q", k, got[k].Type, wt)
				}
			}
			for k, wd := range c.wantDesc {
				if got[k].Description != wd {
					t.Errorf("param %q description = %q, want %q", k, got[k].Description, wd)
				}
			}
		})
	}
}

// TestParseParamsArgRejectsMisplacedActionFields pins the OTHER half of
// the coercion rule: leniency covers a wrong-shaped param VALUE, never a
// key that names no param at all.
//
// The live failure this comes from: an agent fixing a toolbox action sent
// body_template / response_pipe / required nested inside params. Every one
// was coerced into a parameter, the update reported success, the action's
// real required list vanished, and the write-action scaffold rebuilt a
// wrong body from what was left. Eight "successful" updates later the
// toolbox was destroyed and the model had concluded the scaffold was the
// bug. One precise error at the first call ends that.
func TestParseParamsArgRejectsMisplacedActionFields(t *testing.T) {
	misplaced := []struct {
		name string
		in   map[string]any
	}{
		{"body_template", map[string]any{"content": "the body", "body_template": `{"content": {content}}`}},
		{"response_pipe", map[string]any{"content": "the body", "response_pipe": "jq -c '.'"}},
		{"url_template", map[string]any{"content": "the body", "url_template": "/api/v1/posts"}},
		{"command_template", map[string]any{"x": "a value", "command_template": "echo hi"}},
		{"script_body", map[string]any{"x": "a value", "script_body": "print('hi')"}},
		// The exact shape that destroyed the live toolbox.
		{"required list", map[string]any{
			"content":  map[string]any{"type": "string", "description": "Reply content"},
			"post_id":  map[string]any{"type": "string", "description": "Post ID"},
			"required": []any{"post_id", "content"},
		}},
	}
	for _, c := range misplaced {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseParamsArg(c.in)
			if err == nil {
				t.Fatalf("expected a rejection, got params %v", got)
			}
			// The error has to say where the field belongs, or the model
			// retries the same shape.
			if !strings.Contains(err.Error(), "params") {
				t.Errorf("error should point at the right shape, got: %v", err)
			}
		})
	}
}

// TestParseParamsArgKeepsPlausibleParamNames guards the false-positive
// edge: "description", "method", and "name" are real parameter names on
// real APIs, and "required" as a param DESCRIPTOR (not a list) is a
// different thing from the action's required list. None may be rejected.
func TestParseParamsArgKeepsPlausibleParamNames(t *testing.T) {
	ok := map[string]any{
		"description": map[string]any{"type": "string", "description": "Issue description"},
		"method":      "string",
		"name":        "the display name",
		"url":         "string",
		"required":    map[string]any{"type": "boolean", "description": "Is this field mandatory"},
	}
	got, err := parseParamsArg(ok)
	if err != nil {
		t.Fatalf("plausible param names must survive: %v", err)
	}
	for _, k := range []string{"description", "method", "name", "url", "required"} {
		if _, present := got[k]; !present {
			t.Errorf("param %q was dropped", k)
		}
	}
}
