package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// A field with OptionsSource gets its options as the form renders, so a
// long list is read when somebody opens the form and not when the page
// holding it is built. Source-scan, as the other runtime tests are: there
// is no browser here, and what this pins is the form panel still reading
// the key the Go side writes.
func TestAFormFieldCanFetchItsOptionsWhenTheFormRenders(t *testing.T) {
	b, _ := json.Marshal(FormField{Field: "pieces", Type: "checklist", OptionsSource: "api/pieces"})
	if !strings.Contains(string(b), `"options_source":"api/pieces"`) {
		t.Fatalf("the field does not carry its source: %s", b)
	}
	src := mustRuntimePart(t, "10_basics.js")
	if !strings.Contains(src, "f.options_source") || !strings.Contains(src, "function loadOptionSources()") {
		t.Error("the form panel no longer fetches a field's options_source")
	}
	if strings.Index(src, "loadOptionSources().then(") > strings.Index(src, "fetchJSON(cfg.source).then(function(d){ current = d || {}; render(); })") {
		t.Error("options are not fetched before the form renders")
	}
}
