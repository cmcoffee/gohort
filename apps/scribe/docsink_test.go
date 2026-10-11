package scribe

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A page saved from the artifact pane becomes a Private document of the
// user's, titled as the pane named it, its sections split at ## headings; a
// page with no text to keep is refused with where to go instead.
func TestAnArtifactSavesAsADocument(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = saved })
	T := &Scribe{AppCore: AppCore{DB: root}}
	html := `<!doctype html><html><head><title>Page title</title></head><body>
<style>p{color:red}</style><h1>Q3 numbers</h1><p>Revenue grew.</p><h2>Costs</h2><p>Costs fell.</p><script>var secret = 1;</script></body></html>`
	url, stored, err := T.saveFromHTML("u", "Quarterly report", html)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if stored != "Quarterly report" || !strings.HasSuffix(url, "/") {
		t.Errorf("stored %q at %q", stored, url)
	}
	udb := UserDB(root, "u")
	var found *Guide
	for _, k := range udb.Keys(guidesTable) {
		var g Guide
		if udb.Get(guidesTable, k, &g) && g.Title == "Quarterly report" {
			found = &g
		}
	}
	if found == nil {
		t.Fatal("no document was saved")
	}
	for _, sec := range found.Sections {
		if strings.Contains(sec.Markdown, "secret") || strings.Contains(sec.Markdown, "color:red") {
			t.Errorf("a script or style came through as document text: %q", sec.Markdown)
		}
	}
	if !found.Private || len(found.Sections) < 2 {
		t.Errorf("want a Private document split at its headings, got private=%v sections=%d", found.Private, len(found.Sections))
	}
	if _, _, err := T.saveFromHTML("u", "Chart", `<html><body><canvas id="c"></canvas><script>draw()</script></body></html>`); err == nil || !strings.Contains(err.Error(), "Print or Download") {
		t.Errorf("a page with no text should be refused with where to go instead: %v", err)
	}
}
