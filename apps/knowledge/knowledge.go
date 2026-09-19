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
	if s, ok := stewardSection(user, collectionIDFromPath(r.URL.Path)); ok {
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

// stewardSection names the agent in charge of this collection.
//
// Its own section, and deliberately NOT inside the one below. Being in charge
// of a corpus is about this collection and the agent that keeps it current: the
// agent goes and gets things with whatever tools it has, and gets the four
// corpus actions bound to this collection and nothing else. None of that needs
// the collection to be a copy of anywhere.
//
// It used to live in the section below, which renders only when something in
// the deployment can be ENUMERATED — an MCP server with a listing tool, admin
// only. So the ordinary case, "an agent looks after this collection", was
// reachable only after standing up infrastructure it has no use for, and on a
// deployment with none it was not reachable at all.
func stewardSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/steward"
	return ui.Section{
		Title:    "In charge of this collection",
		Subtitle: "An agent can look after this collection.",
			Detail: "It goes and finds material, adds it, and prunes what no longer belongs. Every other agent reads the collection as usual, unchanged.",
		Body: ui.FormPanel{
			Source:  base,
			PostURL: base,
			Fields: []ui.FormField{
				{
					// A select, although the save accepts any string. The
					// stored value is an agent ID and the label is its name,
					// and those are not the same — a free-text box would make
					// a person type the id they can see the name of. An id
					// also survives a rename; what it does not survive is the
					// agent being deleted, and the toolkit's select already
					// handles a stored value whose option has gone rather than
					// silently showing the first one.
					Field: "curator_agent", Type: "select", Label: "Maintained by",
					Options: append([]ui.SelectOption{{Value: "", Label: "(nobody; this collection is filled by hand)"}},
						AgentNameOptions(user)...),
					Help: "That agent may add documents to THIS collection and remove them from it.",
					Detail: "It cannot touch any other collection you own, and it gets no other new powers: it cannot create or change agents, tools or apps.\n\n" +
						"Reading is unaffected. Any agent with this collection attached still searches it exactly as before.",
				},
			},
		},
	}, true
}

