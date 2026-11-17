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

	"encoding/json"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
func (T *KnowledgeApp) WebName() string       { return "Knowledge" }
func (T *KnowledgeApp) WebDesc() string {
	return "Named document collections that travel with your skills."
}

// WebOrder places Knowledge third on the dashboard, right after Agency
// (-1000) and Bridges (-900), ahead of the default-50 app grid.
func (T *KnowledgeApp) WebOrder() int { return -800 }

// knowledgeLegacyPath is where this surface lived as Documents.
const knowledgeLegacyPath = "/documents"

func (T *KnowledgeApp) Routes() {
	// This surface used to be Documents at /documents. Old links still say
	// so. A 308 keeps a POST a POST, and the old path answers before any app
	// grant is checked, so it redirects for everyone rather than refusing
	// the people the hidden redirect app was never granted to.
	RegisterLegacyMount(knowledgeLegacyPath, T.WebPath())
	T.HandleFunc("/", T.handleListPage)
	T.HandleFunc("/c/", T.handleDetailPage)
	// The share picker's candidate list. Served here rather than borrowed:
	// "which users exist" is a question any app can answer from the auth store,
	// so there is no reason for this page's picker to 404 when a sibling app is
	// disabled. The collection itself still lives in orchestrate, which is why
	// the SAVE below goes there.
	T.HandleFunc("/api/user-candidates", jsonapi.Get(T.DB, T.handleUserCandidates))
}

func (T *KnowledgeApp) handleUserCandidates(c *jsonapi.Call) (any, error) {
	// ?groups=1 from a picker whose kind can go to a group as well.
	return json.RawMessage(ShareCandidatesJSON(c.User, c.R.URL.Query().Get("groups") == "1", false)), nil
}

func (T *KnowledgeApp) handleListPage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
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
		// The shared bundle client defines itself and touches no DOM, so it
		// is safe in <head>; the list script below calls it on click.
		ExtraHeadHTML: "<script>" + ArtifactClientJS + "</script>" + bulletinPillsHead + bulletinCardsHead,

		Sections: []ui.Section{
			{
				NoChrome: true,
				Body:     ui.Card{HTML: documentsListBody + newCollectionFormScript(user) + documentsListAssets},
			},
			bulletinsSection(),
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
			Body:     ui.Card{HTML: documentsDetailBody + collectionWatchJS + T.findSourcesScript(user, collectionIDFromPath(r.URL.Path)) + documentsDetailAssets},
		},
	}
	if s, ok := T.stewardSection(user, collectionIDFromPath(r.URL.Path)); ok {
		sections = append(sections, s)
	}
	if s, ok := T.heldSection(user, collectionIDFromPath(r.URL.Path)); ok {
		sections = append(sections, s)
	}
	if s, ok := T.suggestionsSection(user, collectionIDFromPath(r.URL.Path)); ok {
		sections = append(sections, s)
	}
	if s, ok := T.sharingSection(user, collectionIDFromPath(r.URL.Path)); ok {
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
//
// Owner only, like sharingSection and for the same reason: a reader or
// contributor of a shared collection reaches this page too, the steward
// endpoint refuses them, and a form that cannot load is worse than none.
func (T *KnowledgeApp) stewardSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok || c.Owner != user || IsDeploymentScope(c) {
		return ui.Section{}, false
	}
	esc := url.PathEscape(collectionID)
	base := "/orchestrate/api/collections/" + esc + "/steward"
	curate := "/orchestrate/api/collections/" + esc + "/curate"
	plan := "/orchestrate/api/collections/" + esc + "/plan"
	submitters := "/orchestrate/api/collections/" + esc + "/submitters"
	// Auto-Curation is the plan run on a schedule; off, it runs when Run now
	// is pressed. Nothing else is chosen here: no agent is in charge.
	fields := []ui.FormField{
		// What the settings below come to, in one line, kept current as
		// they change.
		{Field: "status", Type: "readonly", Label: "This collection"},
		{Field: "refresh", Type: "select", Label: "Auto-Curation",
			Options: []ui.SelectOption{
				{Value: "never", Label: "Off", Help: "The plan runs only when you press Run now. For a collection you fill once, or by hand."},
				{Value: "month", Label: "Every month", Help: "The plan runs each month, looking for what changed. For reference material that changes slowly."},
				{Value: "week", Label: "Every week", Help: "The plan runs each week, looking for what is new. For a fast-moving subject: news, releases, advisories."},
			},
			Help: "The plan below, run on its own. Run now runs it once either way.",
			Detail: "A run fetches the articles you listed, then links agents suggested, then the search terms; every page it finds is checked before it goes in, and what does not pass waits under Held for you.\n\n" +
				"Reading is unaffected. Any agent with this collection attached still searches it exactly as before."},
	}
	agents := AgentNameOptions(user)
	return ui.Section{
		Title:    "Curation",
		Subtitle: "The plan for filling this collection, and when it runs: once, or on a schedule.",
		Detail:   "One plan fills the collection and keeps it current: run it once to fill it, or on a schedule to keep it up to date. Pages it finds go in only after the check; what does not pass waits under Held for you. Pages you add yourself are never checked.",
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{
				Source:     base,
				PostURL:    base,
				RefreshOn:  []string{base},
				Invalidate: []string{curate},
				Fields:     fields,
			},
			ui.ActionList{
				Source:         plan + "/draft",
				LabelField:     "item",
				DescField:      "desc",
				HistoryField:   "history",
				PostTo:         plan + "/draft",
				ButtonText:     "Draft",
				ProgressSource: plan + "/draft/progress",
				CancelTo:       plan + "/draft/stop",
				EmptyText:      "Drafting a plan needs web search, which is not set up on this server. You can still write the search terms below.",
				// The drafted terms land in the plan below, and change the
				// line at the top.
				Invalidate: []string{plan, plan + "/draft", curate, base},
			},
			ui.FormPanel{
				Source:     plan,
				PostURL:    plan,
				RefreshOn:  []string{plan},
				Invalidate: []string{curate, plan + "/draft", base},
				Fields: []ui.FormField{
					{Field: "search_terms", Type: "rules", Label: "Search terms", AddLabel: "+ Add search terms",
						Placeholder: "No search terms yet: Draft writes them from the description, or add your own below.",
						Help:        "One web search each. A run searches these and keeps what fits."},
					{Field: "filter_rules", Type: "rules", Label: "Rules", AddLabel: "+ Add rule",
						Help: "What to prefer and what to skip, such as \"Prefer docs.vendor.example\" or \"Skip forums\". Read when the search terms are written and when pages are checked."},
					{Field: "docs_per_run", Type: "number", Label: "Documents per run",
						Help: "At most this many are added in one run."},
				},
			},
			ui.ActionList{
				Source:         curate,
				LabelField:     "item",
				DescField:      "desc",
				HistoryField:   "history",
				PostTo:         curate,
				ButtonText:     "Run now",
				ProgressSource: curate + "/progress",
				CancelTo:       curate + "/stop",
				Invalidate:     []string{curate},
			},
			submittersPanel(submitters, agents),
		}},
	}, true
}

// suggestionsSection is the queue of updates agents reading this collection
// have suggested, and the switch that stops them.
//
// For whoever decides about the collection: its owner, or an administrator
// for the deployment's own. The endpoint refuses everybody else, and the
// suggestions carry text out of other people's chats.
func (T *KnowledgeApp) suggestionsSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok {
		return ui.Section{}, false
	}
	if c.Owner != user && !CollectionMaintainer(c, user) && !(c.Owner == "" && IsDeploymentScope(c) && UserIsAdmin(user)) {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID) + "/suggestions"
	return ui.Section{
		Title:    "Suggestions from agents",
		Subtitle: "Agents that read this collection can suggest an update when they learn something it should know. Nothing changes until somebody acts on it.",
		Detail: "A reading agent never writes to the collection itself: one that did would later read its own conclusions back as fact. It files a suggestion naming its source instead.\n\n" +
			"They wait here for you: no agent is in charge of a collection, so nothing acts on a suggestion until somebody does.\n\n" +
			"Only your own agents, and those of people you made contributors, can suggest, and only on their own runs.",
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{
				Source:  base,
				PostURL: base + "/settings",
				Fields: []ui.FormField{{
					Field: "accept_suggestions", Type: "toggle", Label: "Take suggestions from agents",
					Help: "Off: no agent can suggest updates to this collection. Ones already waiting stay.",
				}},
			},
			ui.Table{
				Source:       base,
				RecordsField: "suggestions",
				RowKey:       "id",
				EmptyText:    "No suggestions waiting.",
				Columns: []ui.Col{
					{Field: "text", Label: "Suggestion", Flex: 3},
					{Field: "source", Label: "Source", Flex: 1, Link: "source_link"},
					{Field: "from", Label: "From", Mute: true},
					{Field: "created", Label: "When", Format: "reltime", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Fetch the link", OnlyIf: "source_link", PostTo: base + "/fetch?sid={id}"},
					{Type: "button", Label: "Dismiss", PostTo: base + "/dismiss?sid={id}"},
				},
			},
		}},
	}, true
}

// submittersPanel is the agents that may add documents to the collection:
// which of the owner's agents, and whether each one's documents are checked
// at the door or always allowed. An agent adds; it never takes out.
func submittersPanel(source string, agents []ui.SelectOption) ui.Component {
	if len(agents) == 0 {
		return ui.Card{HTML: `<div style="font-size:var(--fs-sm);color:var(--text-mute)">Agents that submit documents: none of your agents yet. One that fetches documents on a schedule can add them here once you have it.</div>`}
	}
	return ui.FormPanel{
		Source:  source,
		PostURL: source,
		Fields: []ui.FormField{{
			Field: "submitters", Type: "rows", Label: "Agents that submit documents", AddLabel: "+ Add agent",
			Placeholder: "(none: only you and the plan add to it)",
			Help:        "Your agents that may add documents to this collection, such as one that fetches the latest guide on a schedule. They add; they cannot remove anything.",
			Detail: "Checked: every document the agent adds goes through the same check a run of the plan uses, and what does not pass is held for you or turned away. " +
				"Always allow: added as the agent sends it, for a process you trust.\n\n" +
				"An agent that adds under a title already in the collection replaces that document, so it can keep one current version. It can also name the document its own replaces: that one is kept, marked as the older version, and ranked below.",
			Columns: []ui.FormField{
				{Field: "agent", Type: "select", Label: "Agent", Width: 4, Options: append([]ui.SelectOption{{Value: "", Label: "(choose one of your agents)"}}, agents...)},
				{Field: "trust", Type: "select", Label: "Its documents", Width: 3, Options: []ui.SelectOption{
					{Value: "checked", Label: "Checked"},
					{Value: "always", Label: "Always allow"},
				}},
			},
		}},
	}
}

// sharingSection lets the OWNER hand this collection to named people.
//
// Owner only, and gated by loading the record rather than by trusting the page:
// a recipient reaches this same URL, because resolving a shared collection is
// the point, and showing them a share editor would offer a control the endpoint
// refuses. An absent section says "not yours" more honestly than a disabled one.
//
// The middle rung of the governance model. Widening a collection to the whole
// deployment is a different decision with no path today; this is the one people
// actually want, which is handing a corpus to a colleague.
func (T *KnowledgeApp) sharingSection(user, collectionID string) (ui.Section, bool) {
	if strings.TrimSpace(collectionID) == "" || strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	c, ok := LoadCollection(UserDB(CollectionsDB(), user), user, collectionID)
	if !ok || c.Owner != user || IsDeploymentScope(c) {
		return ui.Section{}, false
	}
	base := "/orchestrate/api/collections/" + url.PathEscape(collectionID)
	return ui.Section{
		Title:    "Shared with",
		Subtitle: "Other people, or a team you are on, who may use this collection, each at the access you choose on their chip. Empty means private to you.",
		Detail: "Everybody added starts at READ: it appears in their collections, they can attach it to their agents and search it. " +
			"They cannot rename it, delete it or change who it reaches, and there is only ever one copy, so a document you add later is shared too, and one you remove is gone for everyone.\n\n" +
			"ADD DOCUMENTS lets them upload, paste, fetch articles and research into it. It is worth stopping on: an agent takes this corpus as fact, so whoever adds to it decides what every agent reading it believes.\n\n" +
			"MAINTAIN is that and curating besides: reviewing the suggestions agents file, running the audit, and editing its description, filter rules and articles. Its name, who it reaches, the agent in charge of it and deleting it stay yours.\n\n" +
			"Sharing with the whole deployment is a separate decision and is not offered here.",
		Body: ui.ACLPicker(ui.ACLPickerConfig{
			OptionsSource: "/knowledge/api/user-candidates?groups=1",
			RecordSource:  base,
			Field:         "allowed_users",
			PostTo:        base,
			// PATCH, because the collections endpoint treats an absent field as
			// unchanged: a POST of the whole record from this picker would
			// carry whatever else it happened to have read.
			Method:    "PATCH",
			Noun:      "user",
			Intro:     "People, or a team you are on, who may use this collection. Everybody starts at Read.",
			EmptyText: "No other users to share with yet.",
			// One list with a level per person: Read, Add documents or
			// Maintain. Pickers over overlapping sets (this list and the
			// maintainers it used to have beside it) read as separate
			// decisions and leave you working out which names are in both.
			FlagOffLabel: "Read",
			Levels: []ui.PickerLevel{
				{Field: "contributors", Label: "Add documents",
					Help: "May add documents. An agent takes this corpus as fact, so they decide what every agent reading it believes."},
				{Field: "maintainers", Label: "Maintain",
					Help: "May add documents and curate: review suggestions, run the audit, edit the description, filter rules and articles."},
			},
			Invalidate: []string{base},
		}),
	}, true
}
