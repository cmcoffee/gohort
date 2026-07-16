package temptool

import "testing"

// A no-param tool/action must author without a dummy "_" placeholder:
// parseParamsArg accepts absent, empty-object, and empty-string forms as a
// valid empty param set, while still validating real params when present.
func TestParseParamsArgAllowsEmpty(t *testing.T) {
	empties := []struct {
		name string
		in   any
	}{
		{"nil / absent", nil},
		{"native empty object", map[string]any{}},
		{"stringified empty object", "{}"},
		{"blank string", "   "},
	}
	for _, c := range empties {
		t.Run(c.name, func(t *testing.T) {
			out, err := parseParamsArg(c.in)
			if err != nil {
				t.Fatalf("empty params should be accepted, got error: %v", err)
			}
			if len(out) != 0 {
				t.Fatalf("expected empty param set, got %d params", len(out))
			}
		})
	}
}

func TestParseParamsArgStillValidatesRealParams(t *testing.T) {
	// A provided param with a bad type must still error — relaxing the
	// empty case must not weaken validation of actual params.
	if _, err := parseParamsArg(map[string]any{
		"count": map[string]any{"type": "widget", "description": "x"},
	}); err == nil {
		t.Fatal("unsupported param type should still error")
	}
	// A well-formed param still parses.
	out, err := parseParamsArg(map[string]any{
		"q": map[string]any{"type": "string", "description": "query"},
	})
	if err != nil {
		t.Fatalf("valid param should parse: %v", err)
	}
	if _, ok := out["q"]; !ok {
		t.Fatal("expected param q to be present")
	}
}
