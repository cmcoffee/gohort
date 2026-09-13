package scribe

// The app's declared surface and its routes have to agree. A URL the page
// declares with no route behind it 404s at the moment somebody clicks it, and
// a route nothing points at is dead code — neither shows up in a build.

import (
	"os"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Every endpoint the workbench page names is served, and the direct-edit
// toggle in particular: without its route an article opens an editor whose
// Save fails, which is worse than no editor at all.
func TestPageURLsAreRouted(t *testing.T) {
	page, web := read(t, "page.go"), read(t, "web.go")
	for _, w := range []struct{ decl, route string }{
		{`EditURL:    "body?id={id}"`, `case path == "body":`},
		{`URL: "scribe_import"`, `case path == "import":`},
		{`URL: "scribe_image"`, `case path == "image":`},
		{`URL: "scribe_rules"`, `case path == "rules":`},
	} {
		if !strings.Contains(page, w.decl) {
			t.Errorf("the page no longer declares %s", w.decl)
		}
		if !strings.Contains(web, w.route) {
			t.Errorf("nothing serves %s, so %s 404s when clicked", w.route, w.decl)
		}
	}
	// Each client action the page names must be registered on the same page.
	for _, name := range []string{"scribe_rules", "scribe_import", "scribe_image"} {
		if !strings.Contains(page, `ClientAction("`+name+`"`) {
			t.Errorf("client action %q is referenced but never registered", name)
		}
	}
}

// The app follows its data. Scribe was Guides; the bucket cannot be renamed in
// place, so StoreName must keep pointing at the old one or every guide anyone
// wrote becomes unreachable on upgrade.
func TestStoreNameStaysOnTheOriginalBucket(t *testing.T) {
	if got := (Scribe{}).StoreName(); got != "guides" {
		t.Fatalf("StoreName() = %q — every existing guide lives under %q and a bucket cannot be renamed in place", got, "guides")
	}
	if (Scribe{}).Name() == (Scribe{}).StoreName() {
		t.Error("the test is meaningless if the app name and the store name are the same")
	}
	// The MCP tools reach the same store directly (they run outside a request),
	// so they must resolve it the same way rather than hardcoding a name.
	if !strings.Contains(read(t, "mcp.go"), `RootDB.Bucket((Scribe{}).StoreName())`) {
		t.Error("the MCP tools resolve the store by their own name; they will read an empty bucket")
	}
}

// Both retired paths redirect and carry their grants over. Without this a user
// who could open TechWriter yesterday gets a 404 today and reads it as the app
// having been deleted.
func TestRetiredPathsAreCarriedOver(t *testing.T) {
	app := read(t, "scribe.go")
	for _, path := range []string{`legacyGuidesPath     = "/guides"`, `legacyTechWriterPath = "/techwriter"`} {
		if !strings.Contains(app, path) {
			t.Errorf("missing %s", path)
		}
	}
	for _, call := range []string{
		"RegisterLegacyMount(legacyGuidesPath, T.WebPath())",
		"RegisterLegacyMount(legacyTechWriterPath, T.WebPath())",
		"MigrateAppPathGrants(adb, legacyGuidesPath, T.WebPath())",
		"MigrateAppPathGrants(adb, legacyTechWriterPath, T.WebPath())",
	} {
		if !strings.Contains(app, call) {
			t.Errorf("Routes() does not %s", call)
		}
	}
}

// An article is one body, so every section-shaped write path has to turn one
// away rather than quietly appending a second section to it.
func TestSectionWritesRefuseArticles(t *testing.T) {
	for _, f := range []string{"push_target.go", "curator.go", "mcp.go"} {
		if !strings.Contains(read(t, f), "isArticle()") {
			t.Errorf("%s writes or offers sections without checking for an article", f)
		}
	}
}
