package customapps

import (
	"encoding/json"
	"strings"
	"testing"
)

// An app's page cannot load its own scripts and stylesheets from inside its
// sandboxed frame, so they are put into the page as it is served: a classic
// script where it was, a deferred one at the end of the body, a module as a
// module, a stylesheet as a style element. Anything not one of the app's own
// assets, or missing, is left as written, and a script's text cannot end the
// element early.
func TestAnAppsOwnScriptsAndStylesheetsAreInItsPage(t *testing.T) {
	assets := map[string]string{
		"app.js":   "window.a = '</script><b>x</b>';",
		"late.js":  "window.late = 1;",
		"mod.mjs":  "export const m = 1;",
		"site.css": "body { color: red }",
	}
	read := func(n string) (string, bool) { s, ok := assets[n]; return s, ok }
	html := `<html><head><link rel="stylesheet" href="assets/site.css"><link href="https://cdn.example/x.css" rel="stylesheet"></head><body>
<script src="assets/app.js"></script>
<script defer src="./assets/late.js"></script>
<script type="module" src="assets/mod.mjs"></script>
<script src="https://cdn.example/react.js"></script>
<script src="assets/missing.js"></script>
<p>end</p></body></html>`
	page, _ := json.Marshal(map[string]any{"sections": []any{map[string]any{"body": map[string]any{"type": "frame", "html": html}}}})
	var out struct {
		Sections []struct {
			Body struct {
				HTML string `json:"html"`
			} `json:"body"`
		} `json:"sections"`
	}
	json.Unmarshal(inlineAssetsWith(page, read), &out)
	got := out.Sections[0].Body.HTML
	for _, want := range []string{
		`<style data-asset="site.css">body { color: red }</style>`,
		`href="https://cdn.example/x.css"`,
		`<script data-asset="app.js">window.a = '<\/script><b>x</b>';</script>`,
		`<script type="module" data-asset="mod.mjs">export const m = 1;</script>`,
		`<script src="https://cdn.example/react.js"></script>`,
		`<script src="assets/missing.js"></script>`,
		`<p>end</p><script data-asset="late.js">window.late = 1;</script></body>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `src="assets/app.js"`) || strings.Contains(got, "./assets/late.js") {
		t.Errorf("an own script is still loaded by reference:\n%s", got)
	}

	// A page with no asset reference is served as it was, byte for byte.
	plain := json.RawMessage(`{"sections":[{"body":{"type":"frame","html":"<p>hi</p>"}}]}`)
	if string(inlineAssetsWith(plain, read)) != string(plain) {
		t.Error("a page with no assets was rewritten")
	}
}
