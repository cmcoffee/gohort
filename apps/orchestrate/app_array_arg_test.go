package orchestrate

import (
	"strings"
	"testing"
)

// A list argument arrives as an array, as that array in a JSON string, or as
// one object; anything else is refused with a note, never read as "none".
func TestAppListArgsTakeTheShapesModelsSend(t *testing.T) {
	for _, raw := range []any{
		[]any{map[string]any{"name": "a"}},
		`[{"name": "a"}]`,
		map[string]any{"name": "a"},
		` {"name": "a"} `,
	} {
		arr, note, ok := appArrayArg(raw, "data_sources")
		if !ok || note != "" || len(arr) != 1 {
			t.Errorf("%#v: %v %q %v", raw, arr, note, ok)
		}
	}
	for _, raw := range []any{"low_stock", true, 3.0} {
		if _, note, ok := appArrayArg(raw, "actions"); ok || !strings.HasPrefix(note, "actions IGNORED:") || !strings.Contains(note, "stored before is kept") {
			t.Errorf("%#v: %q %v", raw, note, ok)
		}
	}
}

// An OK against an empty store says it only shows the script runs.
func TestAnEmptyStoreOKSaysWhatItProves(t *testing.T) {
	if !strings.Contains(emptyStoreNote(nil), "EMPTY store") || emptyStoreNote([]map[string]any{{"a": 1}}) != "" {
		t.Fatal("empty-store note")
	}
}
