package ui

import (
	"strings"
	"testing"
)

// The header used to carry ONE link that had to be both "the page you came
// from" and "the way out", and it resolved the ambiguity by guessing from
// history. Two arrows, each with one job, is the fix — so both have to be in
// the runtime, and the dashboard one has to read its target from config rather
// than assuming a path.
func TestHeaderRendersBothArrows(t *testing.T) {
	js := string(runtimeJS)
	for _, want := range []string{
		"'ui-back-link ui-home-link'", // « — the dashboard
		"cfg.home_url",                // ...whose target is configurable
		"'ui-back-group'",             // the pair, grouped
		"title: 'Back'",               // ← — the previous page
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the header runtime is missing %s — the two arrows are how a reader chooses between the previous page and the dashboard", want)
		}
	}
	// The retrace behavior is what makes ← mean "where I actually was" on a hub
	// of peer apps, and it is easy to delete by accident while editing around it.
	if !strings.Contains(js, "uiArrivedFromInsideTheApp()") {
		t.Error("the back arrow no longer checks for an in-app trail; it will follow the declared parent even when history knows better")
	}
}

// A page that declares no HomeURL still gets the dashboard arrow — the whole
// point is that it is there from anywhere, without every app opting in.
func TestPageConfigDefaultsHomeURL(t *testing.T) {
	html := configJSON(t, Page{Title: "A Page", BackURL: "/", ShowTitle: true})
	if !strings.Contains(html, `"home_url":"/"`) {
		t.Errorf("page config should default home_url to the dashboard, got:\n%s", firstConfigLine(html))
	}

	custom := configJSON(t, Page{Title: "A Page", BackURL: "/section", HomeURL: "/hub"})
	if !strings.Contains(custom, `"home_url":"/hub"`) {
		t.Errorf("an explicit HomeURL should survive into the config, got:\n%s", firstConfigLine(custom))
	}
}

// configJSON renders a page's embedded config, which is what the runtime reads
// to build the header.
func configJSON(t *testing.T, p Page) string {
	t.Helper()
	blob, err := p.ConfigJSON()
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	return string(blob)
}

// firstConfigLine pulls out enough of the config to make a failure readable
// without dumping the whole thing into the test log.
func firstConfigLine(html string) string {
	i := strings.Index(html, "back_url")
	if i < 0 {
		return "(no back_url in the rendered page)"
	}
	end := i + 200
	if end > len(html) {
		end = len(html)
	}
	return html[i:end]
}
