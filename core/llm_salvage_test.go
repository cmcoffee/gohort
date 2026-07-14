package core

import (
	"reflect"
	"testing"
)

// The observed failure: llama.cpp missed an inline "</parameter>" close
// tag and terminated the question value at the options parameter's
// close instead, swallowing the whole options block into question and
// dropping the options key from the arguments.
func TestSalvageSwallowedParams(t *testing.T) {
	args := map[string]any{
		"question": "Here's the plan.\n\nSound good?</parameter>\n<parameter=options>\n[\"yes\", \"edit\", \"no\"]",
	}
	salvageSwallowedParams(args)
	if got := args["question"]; got != "Here's the plan.\n\nSound good?" {
		t.Errorf("question = %q", got)
	}
	want := []any{"yes", "edit", "no"}
	if got, ok := args["options"].([]any); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("options = %#v, want %#v", args["options"], want)
	}
}

func TestSalvageSwallowedParamsCases(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "prose mentioning the tag is untouched",
			in:   map[string]any{"question": "What does </parameter> mean in Qwen's format, and why does it matter?"},
			want: map[string]any{"question": "What does </parameter> mean in Qwen's format, and why does it matter?"},
		},
		{
			name: "trailing wrapper residue with no params is stripped",
			in:   map[string]any{"question": "Proceed?</parameter>\n</function>\n</tool_call>"},
			want: map[string]any{"question": "Proceed?"},
		},
		{
			name: "multiple swallowed params, truncated last value",
			in:   map[string]any{"question": "Pick one?</parameter>\n<parameter=multi>\ntrue\n</parameter>\n<parameter=options>\n[\"a\", \"b\"]"},
			want: map[string]any{"question": "Pick one?", "multi": true, "options": []any{"a", "b"}},
		},
		{
			name: "existing keys are never overwritten",
			in:   map[string]any{"question": "Go?</parameter>\n<parameter=multi>\ntrue\n</parameter>", "multi": false},
			want: map[string]any{"question": "Go?", "multi": false},
		},
		{
			name: "legit tag mention before a real swallow point",
			in:   map[string]any{"question": "Why did </parameter> leak last time? Anyway — proceed?</parameter>\n<parameter=options>\n[\"yes\", \"no\"]\n</parameter>"},
			want: map[string]any{"question": "Why did </parameter> leak last time? Anyway — proceed?", "options": []any{"yes", "no"}},
		},
		{
			name: "non-string values ignored",
			in:   map[string]any{"count": float64(3)},
			want: map[string]any{"count": float64(3)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			salvageSwallowedParams(tc.in)
			if !reflect.DeepEqual(tc.in, tc.want) {
				t.Errorf("got %#v, want %#v", tc.in, tc.want)
			}
		})
	}
}
