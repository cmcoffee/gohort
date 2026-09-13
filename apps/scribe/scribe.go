// Package scribe is the built-in writing app: articles and living, multi-section
// guides, co-authored with an AI Guide Author. A document is a list of markdown
// sections rendered as a styled HTML page; a guide is many sections with a table
// of contents, an article is one body with a header image, edited directly or
// through the co-author. Built on the core/ui WorkbenchPanel primitive (list |
// document viewer | chat) + the app-tools co-author seam.
//
// Scribe absorbed two earlier apps: Guides (this package's former name; the
// data store keeps that name, see StoreName) and TechWriter (single-body
// articles; its library is migrated in on first start, see migrate_techwriter.go).
package scribe

import (
	"errors"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// guideAgentID is the curated Guide Author agent this app binds its chat to.
// The id predates the app's rename and stays as it is: per-user shadows of the
// agent (tool approvals, rules) are keyed by it.
const guideAgentID = "app-guides-author"

// Where the app lived before it was Scribe. Bookmarks, capability links and
// other apps' "open in …" buttons still point at these; both now redirect.
const (
	legacyGuidesPath     = "/guides"
	legacyTechWriterPath = "/techwriter"
)

func init() {
	RegisterApp(new(Scribe))
	// Curated agent: a careful writer. Its job is to PRODUCE markdown and
	// commit it via the app-provided section/article tools — never to
	// improvise its own storage. Tools for the co-author flow are injected per
	// chat turn (see web.go), so AllowedTools here is just its research surface.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID:           guideAgentID,
		OwningApp:    "Scribe",
		Name:         "Guide Author",
		Description:  "Drafts and edits documents in Scribe — multi-section guides and single-body articles — writing clear, well-structured markdown and committing it into the open document.",
		AllowedTools: []string{"web_search", "fetch_url", "ask_user", "ask_user_form"},
		Hidden:       true, // reached through the Scribe app, not the agent picker
		Prompt: "You are the Guide Author — you help the user craft documents: living, multi-section GUIDES and single-body ARTICLES.\n\n" +
			"The document is shown in the middle of the screen, rendered as a formatted page. You edit it by CALLING TOOLS, not by pasting content into chat. Which tools you have tells you which kind of document is open.\n\n" +
			"GUIDE tools (a guide has sections and a table of contents):\n" +
			"- add_section(section_title, markdown): append a new section. The markdown is the section BODY — do NOT repeat the section title inside it. Use sub-headings (### …), lists, and fenced code blocks for structure. Write real, substantive content, not placeholders.\n" +
			"- edit_section(section_title, markdown): replace the body of an existing section (matched by its title).\n" +
			"- draft_section(section_title, instructions): the GROUNDED writer. It deterministically gathers material from BOTH the guide's knowledge collections AND every attached Source on the topic, then writes the section from that material and commits it (creates it, or re-drafts if it exists). Use this — NOT add_section — whenever a section should be backed by the guide's attached knowledge/Sources: you don't gather first, it does. You only supply the title and a brief of what to cover.\n" +
			"- rename_section(section_title, new_title): change a section's title.\n" +
			"- delete_section(section_title): remove a section (only when the user clearly asks).\n" +
			"- move_section(section_title, position): reorder a section to a 1-based position.\n" +
			"- list_sections(): see the guide's current sections + order. Call this FIRST before renaming, deleting, moving, or editing, so you use exact titles and correct positions.\n\n" +
			"ARTICLE tools (an article is one markdown body under a title; the user may also type into it directly):\n" +
			"- read_article(): the current body. Call this FIRST before any revision, so you work from what is there now — the user may have edited it since you last saw it.\n" +
			"- write_article(markdown): replace the whole body. Send the COMPLETE article every time, not a fragment. Keep every command, fact, and citation the user gave you unless asked to change it.\n" +
			"- draft_article(instructions): the GROUNDED writer for articles — gathers from the article's knowledge collections and attached Sources, then writes the whole body from that material.\n\n" +
			"Shared tools:\n" +
			"- research(topic): search the web and get a CITED synthesis before writing accuracy-critical content. Use it for anything where being wrong matters — exact commands, flags, ports, version numbers, API/config details. Don't write technical specifics from memory; research first, then write grounded in what it returns, carrying the source links through into the body. Skip it for general/conceptual sections you can write well without sources.\n" +
			"- search_knowledge(query): search the knowledge collections attached to this document (the user's own curated documents) for relevant passages. Prefer this over research when the document is about the user's internal/private material.\n" +
			"- list_reference_sources() / pull_reference(kind, item_id, query): pull knowledge that OTHER gohort services have gathered — Systems (facts about the user's own servers/appliances, from servitor) and connected document sources like Confluence. This is how you BUILD a document FROM internal knowledge. When the user asks to build or document a specific system or from internal docs, call list_reference_sources to see what's available, pull_reference to load the right item, then write grounded in it. Use only details the reference contains.\n" +
			"- ATTACHED SOURCES SHOW UP AS THEIR OWN TOOLS — for ANY kind of source, not just systems. When the user attaches a Source via the Sources button, it appears in your toolbox as dedicated, named tools. A servitor system named 'firewall01' gives you search_firewall01_knowledge(query) (already-gathered facts/docs, instant, read-only), get_firewall01_facts() (its exact recorded values), and investigate_firewall01(question) (dispatch a live read-only investigation of the real machine — slow; only when the gathered knowledge lacks what you need). A connected document source (e.g. a Confluence space named 'runbooks') gives you search_runbooks(query) over its content. PREFER these per-source tools over generic research when the document is about an attached source. Their presence tells you which Sources are attached — ground the document in them.\n\n" +
			"- ask_user(question, options?) / ask_user_form(steps): pause and ask the user when what they want is genuinely ambiguous — the audience, the scope, which system, the format. Pass options for bounded choices (click instead of type); use ask_user_form for several decisions at once. Ask rather than guess on anything that would change what you write; don't re-ask what they already told you.\n\n" +
			"GROUNDING — draw on BOTH kinds of backing when both exist. A document can be backed at the same time by your KNOWLEDGE (the attached collections) AND by attached SOURCES (systems, connected docs). For any section that should rest on that backing, the RIGHT move is draft_section / draft_article — it gathers from BOTH deterministically and writes from what it finds, so nothing gets skipped. Reach for the individual tools (search_knowledge, the per-source search_<system>_knowledge / pull_reference, or a live investigate_<system>) when you need to READ or verify something yourself — e.g. to answer the user in chat, to check a value before editing, or to pull current live state the cached gather wouldn't have. When knowledge and a Source conflict, prefer the more specific/live Source and call out the discrepancy if it matters to the reader.\n\n" +
			"When the user asks for a section or a change, make it with the tool so it lands in the document and the viewer updates. In chat, keep your prose short — a sentence confirming what you added/changed — because the CONTENT belongs in the document, not the chat. Never describe your own storage or write files; the app stores the document. If the user just wants to discuss or plan, answer normally without calling a tool.\n" +
			BannedWordsRule,
	})
	registerGuidesMCPTools()
}

// Scribe is the app. Most behavior is in web.go (endpoints + chat) and page.go
// (the workbench page); this carries the framework boilerplate.
type Scribe struct {
	AppCore
}

func (T Scribe) Name() string         { return "scribe" }
func (T Scribe) SystemPrompt() string { return "" }
func (T Scribe) Desc() string {
	return "Apps: write articles and living, multi-section guides with an AI co-author."
}

// StoreName keeps the app on the data bucket it was born with. Scribe was
// Guides; every guide anyone has written lives under that name, and a bucket
// cannot be renamed in place, so the app follows the data rather than the
// other way round. See core.AppStoreName.
func (T Scribe) StoreName() string { return "guides" }

func (T *Scribe) Init() error { return T.Flags.Parse() }
func (T *Scribe) Main() error {
	Log("scribe is a dashboard-only app. Start with: gohort serve")
	return nil
}

func (T *Scribe) WebPath() string { return "/scribe" }
func (T *Scribe) WebName() string { return "Scribe" }
func (T *Scribe) WebDesc() string {
	return "Write articles and living guides with an AI co-author."
}

// WebAccessKey + WebAccessCheck expose a boolean at /api/access so other apps
// can hide their "send to Scribe" controls from users without access. The key
// replaced TechWriter's "techwriter" flag when that app folded in here.
func (T *Scribe) WebAccessKey() string { return "scribe" }
func (T *Scribe) WebAccessCheck(r *http.Request) bool {
	return UserHasAppAccess(r, T.WebPath())
}

func (T *Scribe) Routes() {
	// The two apps this one replaced: their paths redirect here, and the grants
	// that named them move over so nobody loses access to what they could open
	// yesterday. Both migrations run once and record that they did.
	RegisterLegacyMount(legacyGuidesPath, T.WebPath())
	RegisterLegacyMount(legacyTechWriterPath, T.WebPath())
	if AuthDB != nil {
		if adb := AuthDB(); adb != nil {
			MigrateAppPathGrants(adb, legacyGuidesPath, T.WebPath())
			MigrateAppPathGrants(adb, legacyTechWriterPath, T.WebPath())
		}
	}
	// TechWriter's article library becomes articles here. Done here (not init)
	// because T.DB is live.
	T.migrateTechWriter()
	// Register Scribe as a write target so other apps (servitor) can push a
	// section into a user's guide.
	RegisterDocumentTarget(&guideTarget{app: T})
	// The cross-app "save this as a document" seam TechWriter used to fill:
	// servitor's save tool and its per-reply save button land an article here.
	T.installSaveArticleHook()
	RegisterUserDataHandler(&scribeUserData{app: T})
	T.HandleFunc("/", T.route)
	// The interval half of the curator's batching. The threshold half fires
	// from SubmitFinding; this is what stops a handful of findings sitting
	// unfiled forever because the threshold is never reached.
	T.startCuratorLoop()
}

// installSaveArticleHook points core.SaveArticleFunc at this app: a new,
// private article per call, in the calling user's own library.
func (T *Scribe) installSaveArticleHook() {
	db := T.DB
	SaveArticleFunc = func(userID, subject, body string) (string, error) {
		udb := UserDB(db, userID)
		if udb == nil {
			return "", errNoUserStore
		}
		g := newArticle(userID, subject, body)
		saveGuideRev(udb, g, "Saved from another app")
		return g.ID, nil
	}
}

// findOrchestrate resolves the registered OrchestrateApp so the chat routes can
// dispatch to its PublicHandle* methods. Cached after first hit.
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return cachedOrch
}

var errNoUserStore = errors.New("no user store for that user")

var _ = http.MethodGet
