package temptool

import "testing"

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
