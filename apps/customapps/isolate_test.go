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

// An app's page cannot use the document workbench's two routes to raw HTML:
// a record body taken as HTML is rendered as markdown, and the HTML history
// preview is not offered.
func TestAnAppsPageWritesNoRawHTML(t *testing.T) {
	page := json.RawMessage(`{"sections":[{"body":{"type":"workbench","body_is_html":true,"history":{"preview_url":"data/rev?id={id}"}}}]}`)
	out := string(isolateAppHTML(page))
	if strings.Contains(out, `"body_is_html":true`) || strings.Contains(out, "preview_url") {
		t.Errorf("the page still writes raw HTML: %s", out)
	}
}

// Every endpoint an app's page fetches or posts to stays inside the app: one
// naming another gohort endpoint is blanked, while the app's own relative
// (or absolute, under its address) endpoints, client action names and plain
// links pass unchanged.
func TestAnAppsPageEndpointsStayInTheApp(t *testing.T) {
	page := json.RawMessage(`{"sections":[
	 {"body":{"type":"table","source":"records","row_actions":[
	   {"label":"Approve","post_to":"/orchestrate/api/console/credential-key/replace","method":"POST"},
	   {"label":"Mine","post_to":"action/close?id={id}","method":"POST"},
	   {"label":"Open","post_to":"https://example.com/{id}","method":"GET"},
	   {"label":"Local","post_to":"customapps_share","method":"client"}]}},
	 {"body":{"type":"toolbar","actions":[
	   {"label":"Go","url":"/orchestrate/api/confirm"},
	   {"label":"Climb","url":"data/%2e%2e/%2e%2e/admin","method":"POST"},
	   {"label":"Old","url":"/customapps/tracker/action/sync","method":"POST"}]}},
	 {"body":{"type":"form","source":"/extensions/api/credentials","post_url":"records"}}]}`)
	out := string(isolateAppHTML(page, "/customapps/tracker/"))
	for _, gone := range []string{"/orchestrate/api/console/credential-key/replace", "/orchestrate/api/confirm", "%2e%2e", "/extensions/api/credentials"} {
		if strings.Contains(out, gone) {
			t.Errorf("an endpoint outside the app survived: %s", gone)
		}
	}
	for _, kept := range []string{`"records"`, "action/close?id={id}", "https://example.com/{id}", "customapps_share", "/customapps/tracker/action/sync"} {
		if !strings.Contains(out, kept) {
			t.Errorf("the app's own %s was blanked: %s", kept, out)
		}
	}
}
