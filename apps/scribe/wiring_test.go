package scribe

// The app's declared surface and its routes have to agree. A URL the page
// declares with no route behind it 404s at the moment somebody clicks it, and
// a route nothing points at is dead code — neither shows up in a build.

import (
	"os"
	"regexp"
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

// The "⋯" in a sessions list header is built only for a panel that declares
// something to put in it. Scribe declared neither, so the column that holds a
// year of drafting conversations had no overflow where every other chat column
// has one, and clearing old sessions meant one at a time.
//
// Bulk delete rides DeleteURL. Asserting both together is the point: opting in
// without the endpoint gives a Select mode whose delete goes nowhere.
func TestPastSessionsCanBeClearedInBulk(t *testing.T) {
	page := read(t, "page.go")
	if !strings.Contains(page, "BulkSelect:") {
		t.Error("the sessions list has no Select mode, so its header builds no overflow menu at all")
	}
	if !strings.Contains(page, `DeleteURL:    "chat/sessions/{id}"`) {
		t.Error("bulk delete fires at DeleteURL; without it Select mode is a gesture with no effect")
	}
}

// Each header holds controls of its own scope. Settings edits the OPEN
// document's name, privacy and sharing, so it belongs with the viewer bar
// beside Publish and Image — not in the list header, where it read as a
// control over the library and helped overflow a 200px column.
func TestSettingsSitsWithTheDocumentItEdits(t *testing.T) {
	page := read(t, "page.go")
	list := between(t, page, "ListActions: []ui.WorkbenchAction{", "\n\t\t},")
	viewer := between(t, page, "ViewerActions: []ui.WorkbenchAction{", "\n\t\t},")
	if strings.Contains(list, `"Settings"`) {
		t.Error("Settings is back in the list header; it acts on the open document, not the library")
	}
	if !strings.Contains(viewer, `"Settings"`) {
		t.Error("Settings left the list header without arriving in the viewer bar")
	}
	// Import CREATES a document, so it sits beside the other control that
	// creates one rather than in the bar that acts on whatever is open.
	if !strings.Contains(list, `"Import"`) {
		t.Error("Import creates a document and belongs beside + New")
	}
}

// What an action is ABOUT decides when it is usable, separately from where it
// sits. Both bars gated everything in them on a selection, which made the two
// actions that matter most on an empty library — import into it, set the rules
// it is written under — the two that could not be clicked.
func TestLibraryActionsDoNotFollowTheSelection(t *testing.T) {
	page := read(t, "page.go")
	for _, lbl := range []string{"Rules", "Import"} {
		re := regexp.MustCompile(`\{Label: "` + lbl + `"[^}]*Scope: "library"`)
		if !re.MatchString(page) {
			t.Errorf("%s is about the library, so it must be library-scoped or it greys out with nothing selected", lbl)
		}
	}
	// Rules moved to the viewer bar and must still not be gated there — the
	// whole point of the scope is that placement and gating are separate.
	viewer := between(t, page, "ViewerActions: []ui.WorkbenchAction{", "\n\t\t},")
	if !strings.Contains(viewer, `"Rules"`) {
		t.Error("Rules is tuned while reading what the agent wrote; it belongs in the viewer bar")
	}
}

func between(t *testing.T, s, start, end string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("%q not found in page.go", start)
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("could not find %q after %q", end, start)
	}
	return rest[:j]
}

// Which document a turn is about comes from the REQUEST, on every path that has
// one. The stored marker is one slot per user, written fire-and-forget when a
// document is opened, so a send that beats that write edits — and files its
// session under — the document the author just left.
func TestTheOpenDocumentTravelsWithTheRequest(t *testing.T) {
	page := read(t, "page.go")
	for _, want := range []string{`"chat/sessions?guide={scope}"`, `"chat/send?guide={scope}"`} {
		if !strings.Contains(page, want) {
			t.Errorf("%s must carry the open document; without it the server falls back to a remembered one", want)
		}
	}
	web := read(t, "web.go")
	// The send path resolves ONCE and uses that answer for the tools and for
	// the session stamp — two reads could disagree with each other.
	if strings.Count(web, "guideID := requestGuideID(r, udb)") != 1 {
		t.Error("handleChatSend should resolve the open document exactly once per turn")
	}
	if !strings.Contains(web, "stampAppContext(r, guideID)") {
		t.Error("the session is still filed under the remembered document, so Past sessions can be right and still show the wrong thing")
	}
	// And no request-driven path may reach past it to the marker directly.
	for _, stale := range []string{
		"T.resolve(r, udb, user, activeGuideID(udb))",
		"stampAppContext(r, activeGuideID(udb))",
		"PublicHandleSessionListFor(w, r, agent.ID, activeGuideID(udb))",
	} {
		if strings.Contains(web, stale) {
			t.Errorf("a request-driven path reads the stored marker directly: %s", stale)
		}
	}
}

// The Publisher's send is the other send path, and the one with the worst
// failure: it pushes a document into a team wiki under the deployment's
// branding, so a stale answer publishes the WRONG document to a real place for
// other people. Its panel is mounted in a modal rather than in the workbench,
// so it carries the id directly rather than through {scope}.
func TestThePublisherIsToldWhichDocument(t *testing.T) {
	page := read(t, "page.go")
	if !strings.Contains(page, "'publish/chat/send?guide=' + encodeURIComponent(gid)") {
		t.Error("the Publisher's send names no document, so it falls back to whichever one the server remembers")
	}
	pub := read(t, "publish.go")
	if strings.Contains(pub, "id := activeGuideID(udb)") {
		t.Error("openPublishDocument reads the remembered document rather than the one the request names")
	}
	if !strings.Contains(pub, "id := requestGuideID(r, udb)") {
		t.Error("openPublishDocument should resolve through requestGuideID")
	}
}

// Background work legitimately uses the marker — it set the marker itself and
// has no request to read. The empty Guide is that case, said out loud.
func TestBackgroundToolBuildsFallBackToTheMarker(t *testing.T) {
	cur := read(t, "curator.go")
	if !strings.Contains(cur, "coauthorScope{Ctx: ctx, UDB: udb, Orch: orch, User: user, CanEdit: true}") {
		t.Error("the curator's tool build should pin no guide — it runs against the marker it set")
	}
}
