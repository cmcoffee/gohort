package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A non-JSON content type (XML/CalDAV/SOAP) must substitute placeholders VERBATIM
// — the CalDAV bug was JSON-encoding turning {start_date} into ""2026-07-22""
// and then failing JSON validation on the leading '<'.
func TestRawBodyContentType(t *testing.T) {
	t.Run("isJSONContentType", func(t *testing.T) {
		for _, ct := range []string{"", "application/json", "application/vnd.api+json", "APPLICATION/JSON"} {
			if !isJSONContentType(ct) {
				t.Errorf("%q should be treated as JSON", ct)
			}
		}
		for _, ct := range []string{"application/xml", "text/xml", "text/plain"} {
			if isJSONContentType(ct) {
				t.Errorf("%q should NOT be treated as JSON", ct)
			}
		}
	})

	params := map[string]ToolParam{"start_date": {Type: "string"}, "end_date": {Type: "string"}}
	required := []string{"start_date", "end_date"}

	t.Run("verbatim substitution, no quoting", func(t *testing.T) {
		tmpl := `<c:time-range start="{start_date}T00:00:00Z" end="{end_date}T23:59:59Z"/>`
		got, err := substituteRaw(tmpl, params, required, map[string]any{"start_date": "2026-07-22", "end_date": "2026-07-22"})
		if err != nil {
			t.Fatal(err)
		}
		want := `<c:time-range start="2026-07-22T00:00:00Z" end="2026-07-22T23:59:59Z"/>`
		if got != want {
			t.Errorf("substituteRaw = %q, want %q", got, want)
		}
	})

	t.Run("missing required errors", func(t *testing.T) {
		if _, err := substituteRaw(`{start_date}`, params, required, map[string]any{}); err == nil {
			t.Error("expected an error for a missing required placeholder")
		}
	})

	t.Run("optional-absent drops, unknown token preserved", func(t *testing.T) {
		p := map[string]ToolParam{"a": {Type: "string"}}
		if got, _ := substituteRaw(`X{a}Y`, p, nil, map[string]any{}); got != "XY" {
			t.Errorf("optional-absent = %q, want XY", got)
		}
		if got, _ := substituteRaw(`keep {unknown} literal`, p, nil, map[string]any{}); got != "keep {unknown} literal" {
			t.Errorf("unknown token = %q, want it preserved", got)
		}
	})
}
