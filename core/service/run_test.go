package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// An output's other places are outputs of their own, one level deep, and
// single is for a json output read as one item.
func TestOutputAlternativesAreChecked(t *testing.T) {
	spec := func(vectors map[string]any) map[string]any {
		s := embedSpec("m1")
		s["provides"].([]any)[0].(map[string]any)["output"] = map[string]any{"vectors": vectors}
		return s
	}
	for _, c := range []struct {
		vectors map[string]any
		want    string
	}{
		{map[string]any{"path": "data", "or": []any{map[string]any{"path": "a", "or": []any{map[string]any{"path": "b"}}}}}, "has an or of its own"},
		{map[string]any{"path": "data", "or": []any{map[string]any{"path": "x", "as": "url"}}}, "or 1"},
		{map[string]any{"path": "data", "each": "embedding", "single": true}, "single reads one item"},
	} {
		raw, _ := json.Marshal(spec(c.vectors))
		s, err := Parse(raw)
		if err == nil {
			err = s.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.vectors, err)
		}
	}
}
