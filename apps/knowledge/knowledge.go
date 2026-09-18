// Package knowledge serves the top-level Knowledge surface — named
// collections of documents that get attached to skills, so when a
// skill activates the agent's RAG search also covers its docs.
// Collection data + APIs live in orchestrate
// (/orchestrate/api/collections/*); this package only renders the UI.
//
// Distinct from the per-agent Knowledge toolbar button: the agent
// button manages one agent's own corpus (shared admin docs + private
// uploads + auto-inferred wipe). This surface is cross-cutting —
// one collection, attached to N skills, recalled by every agent that
// has those skills enabled. Same name, different scope; context (top-
// level nav vs. button on an agent page) tells the user which they
// are looking at.
//
// History note: this app was previously called "Documents" and lived
// at /documents/. Renamed because the surface is more about creating
// knowledge bundles attached to skills than about file management.
// A back-compat redirect at /documents/* → /knowledge/* keeps old
// URLs working.

package knowledge

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(KnowledgeApp)) }

// KnowledgeApp is a UI-only shell — backend lives in orchestrate.
type KnowledgeApp struct {
	AppCore
}

func (T KnowledgeApp) Name() string         { return "knowledge" }
func (T KnowledgeApp) SystemPrompt() string { return "" }
func (T KnowledgeApp) Desc() string {
	return "Knowledge: named document collections that travel with your skills, recalled by every agent that has those skills enabled."
}

func (T *KnowledgeApp) Init() error { return T.Flags.Parse() }
func (T *KnowledgeApp) Main() error {
	Log("Knowledge is a web-only app. Start with:\n  gohort serve :8080")
	return nil
}

func (T *KnowledgeApp) WebPath() string { return "/knowledge" }

// HubTab makes Knowledge a member of the shared top-nav hub.
func (T *KnowledgeApp) HubTab() (string, int) { return "Knowledge", 30 }
func (T *KnowledgeApp) WebName() string { return "Knowledge" }
func (T *KnowledgeApp) WebDesc() string {
	return "Named document collections that travel with your skills."
}

// WebOrder places Knowledge third on the dashboard, right after Agency
// (-1000) and Bridges (-900), ahead of the default-50 app grid.
func (T *KnowledgeApp) WebOrder() int { return -800 }

func (T *KnowledgeApp) Routes() {
	T.HandleFunc("/", T.handleListPage)
	T.HandleFunc("/c/", T.handleDetailPage)
}

func (T *KnowledgeApp) handleListPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// Sub-mux StripPrefix means r.URL.Path is relative to /knowledge.
	// Only the root path itself renders the list; everything else 404s.
	if r.URL.Path != "/" && r.URL.Path != "" {
		http.NotFound(w, r)
		return
	}
	// Bundle CSS + JS INTO the Card body. The Card component
	// re-executes inline <script> tags after inserting its HTML, so
	// our DOM lookups (#docs-new, #docs-list) hit elements that
	// actually exist. Putting the script in ExtraHeadHTML runs it
	// during <head> parse — before the body is in the DOM — which
	// silently broke addEventListener wiring.
	page := ui.Page{
		Title:     "Knowledge",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "920px",
		Nav:       HubNav("/knowledge"), // shared hub tabs, Knowledge active

		Sections: []ui.Section{
			{
				NoChrome: true,
				Body:     ui.Card{HTML: documentsListBody + documentsListAssets},
			},
		},
	}
	page.ServeHTTP(w, r)
}

func (T *KnowledgeApp) handleDetailPage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sections := []ui.Section{
		{
			NoChrome: true,
			Body:     ui.Card{HTML: documentsDetailBody + documentsDetailAssets},
		},
	}
	if s, ok := curationSection(user, collectionIDFromPath(r.URL.Path)); ok {
		sections = append(sections, s)
	}
	page := ui.Page{
		Title:     "Collection",
		ShowTitle: true,
		BackURL:   "/knowledge/",
		Nav:       HubNav("/knowledge"), // shared hub tabs, Knowledge active
		MaxWidth:  "920px",
		Sections:  sections,
	}
	page.ServeHTTP(w, r)
}

// collectionIDFromPath reads the id out of /knowledge/c/<id>. The page's own
// script parses the same path client-side; this is the server needing it too,
// to address the endpoints a rendered form talks to.
func collectionIDFromPath(path string) string {
	_, rest, found := strings.Cut(path, "/c/")
	if !found {
		return ""
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	id, err := url.PathUnescape(rest)
	if err != nil {
		return rest
	}
	return id
}

// curationSection is the binding editor: which source this collection is a
// copy of, how the last sync went, and a way to run one now.
//
// Rendered only when there is somewhere to bind TO. A deployment with no
// enumerable source would otherwise get a control whose picker is empty and
// whose explanation is a sentence about a feature it cannot use.
func curationSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" {
		return ui.Section{}, false
	}
	choices := CuratableSourceOptions(user)
	if len(choices) == 0 {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/curate"
	return ui.Section{
		Title:    "Kept in step with",
		Subtitle: "Make this collection a copy of somewhere else. What the source has is pulled in, what it changes is re-pulled, and what it deletes is retired here too — which is the part filling a collection by hand or from the web cannot do.",
		Body: ui.FormPanel{
			Source:  base,
			PostURL: base,
			// Sync now, through the Test affordance: it posts, prints what came
			// back, and is cancellable while it runs, which a sync over a large
			// space needs and a bespoke button would have to be given.
			TestURL:   base + "/run",
			TestLabel: "Sync now",
			Fields: []ui.FormField{
				{Field: "status", Type: "readonly", Label: "Last sync"},
				{
					Field: "curated_from", Type: "rows", Label: "Sources",
					AddLabel: "Add a source",
					Help:     "Only sources that can list everything they hold are offered. One that can only be searched is left out: a collection bound to it could be added to forever and would never find out that something had been deleted.",
					Columns: []ui.FormField{
						{Field: "source", Type: "select", Label: "Source", Options: choices},
					},
				},
			},
		},
	}, true
}
