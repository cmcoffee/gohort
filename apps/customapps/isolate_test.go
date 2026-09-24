package customapps

// Every Card and Frame in an app's page is served isolated, however deep it
// sits and however old the stored spec is, and may fetch only the app's own
// endpoints.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnAppsPageHTMLIsServedIsolated(t *testing.T) {
	page := json.RawMessage(`{"sections":[{"body":{"type":"card","html":"<script>steal()</script>"}},
		{"body":{"type":"stack","children":[{"type":"frame","html":"<canvas></canvas>"},{"type":"table","source":"records"}]}}]}`)
	out := string(isolateAppHTML(page))
	if strings.Count(out, `"isolate":true`) != 2 {
		t.Fatalf("both the card and the nested frame should be isolated: %s", out)
	}
	if !strings.Contains(out, `"isolate_fetch":["data/"`) {
		t.Errorf("the app's own endpoints should be the only ones allowed: %s", out)
	}
	var v map[string]any
	_ = json.Unmarshal([]byte(out), &v)
	tbl := v["sections"].([]any)[1].(map[string]any)["body"].(map[string]any)["children"].([]any)[1].(map[string]any)
	if _, ok := tbl["isolate"]; ok {
		t.Error("a table is not HTML and should be left alone")
	}
}
