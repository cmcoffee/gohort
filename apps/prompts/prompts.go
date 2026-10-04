// Package prompts is the Prompts hub surface: a document-workbench (list -
// editor - chat, via core/ui's ArticleEditor) over the framework prompt blocks
// that shape agent behavior — the "hidden prompts" made visible AND editable.
// The left list is the registered blocks; the centre editor holds a block's
// effective text; the right chat refines it with the worker LLM. Saving stores
// a deployment-level override that the prompt assembler reads, so an edit
// changes what agents receive on their next turn; saving the default (or an
// empty body) reverts. Blocks come from core's PromptBlock registry (see
// apps/orchestrate/framework_prompts_registry.go).
package prompts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/editor"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterApp(new(PromptsApp))
	// Editing the framework prompt blocks is deployment tuning, not agent
	// behavior — so the editor lives inside the admin UI (the LLMs tab),
	// self-registered here rather than surfaced as an agent-facing hub app. The
	// app's routes below still serve the editor; WebHidden keeps it off the
	// dashboard and there's no HubTab, so it's reached only from admin.
	//
	// Built per request rather than once here, so an action another package
	// adds from its own init (RegisterEditorAction) is on the toolbar: that
	// init can run after this one.
	RegisterAdminSectionSource(func(*http.Request) []AdminSectionEntry {
		return []AdminSectionEntry{{Section: promptsAdminSection(), Head: promptsHeadHTML(), App: "/prompts", Order: editorOrder}}
	})
}

// EditorAction is what another package adds to the Prompt overrides editor:
// a toolbar button and the head script that registers its client action
// (window.uiRegisterClientAction), which is handed the editor handle; or, with
// no button (an empty Label), a head script alone. Such a script can add a
// section to the Check dialog: push a function onto window.promptsCheckExtras
// and it is called with (box, ctx) each time Check opens, box an element of
// its own under the readings, ctx {editor, payload: {id, body, variant},
// isOpen()} so it can stop its work when the dialog closes.
type EditorAction struct {
	Action ui.ToolbarAction
	Head   string
}

var (
	editorActionsMu sync.Mutex
	editorActions   []EditorAction
)

// RegisterEditorAction adds a button to the editor's toolbar, before "Revert
// to default". Call from an init().
func RegisterEditorAction(a EditorAction) {
	editorActionsMu.Lock()
	defer editorActionsMu.Unlock()
	editorActions = append(editorActions, a)
}

func registeredEditorActions() []EditorAction {
	editorActionsMu.Lock()
	defer editorActionsMu.Unlock()
	return append([]EditorAction(nil), editorActions...)
}

// PromptsApp is the app entry point. Embedding AppCore wires in shared state
// (DB, flag set) and the LLM handles the chat pane uses.
type PromptsApp struct {
	AppCore
}

// --- core.Agent interface ----------------------------------------------------

// Pointer receivers, like every other app (OrchestrateApp, FileStoreApp):
// RegisterApp passes a pointer, and a value receiver copies the struct on
// every call, which go vet flags the moment it holds a lock.
func (T *PromptsApp) Name() string         { return "prompts" }
func (T *PromptsApp) SystemPrompt() string { return "" }
func (T *PromptsApp) Desc() string {
	return "Apps: Edit the framework prompt blocks that shape agent behavior."
}
func (T *PromptsApp) Init() error { return T.Flags.Parse() }
func (T *PromptsApp) Main() error {
	Log("Prompts is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// --- core.WebApp interface ---------------------------------------------------

func (T *PromptsApp) WebPath() string { return "/prompts" }
func (T *PromptsApp) WebName() string { return EditorTitle }

// AdminTab is the admin tab the prompt editor lives on: the LLMs tab, with the
// models it prompts and their Optimize. A model's prompts are part of the
// model; a hand edit and an Optimize write the same per-model wording, so
// they live in the same place.
const AdminTab = "LLMs"

// editorOrder places the editor on that tab after Optimize, which registers
// later than this package and so cannot be placed before it by init order.
const editorOrder = 10

// EditorTitle names the editor's section on that tab: the overrides of the
// shipped prompt blocks, beside sections other apps add (Optimize, per-tier
// text).
const EditorTitle = "Prompt overrides"

func (T *PromptsApp) WebDesc() string {
	return "Edit the framework prompt blocks that shape agent behavior."
}

// WebHidden keeps Prompts off the dashboard: it's framework tuning that lives in
// the admin UI (RegisterAdminSection), reached from there — not an agent-facing
// app. The routes still serve, so the admin-embedded editor can call them.
func (T *PromptsApp) WebHidden() bool { return true }

// WebRestricted hides the Prompts card from non-admins on the dashboard —
// editing framework blocks is a deployment-wide operator action.
func (T *PromptsApp) WebRestricted(r *http.Request) bool {
	// RequestIsAdmin, not AuthIsAdmin(T.DB, …): an app's T.DB is
	// global.db.Bucket("<app>"), a namespaced substore, so asking it for
	// the auth table finds nothing. AuthHasUsers(T.DB) was false for the
	// same reason, which short-circuited the whole condition — this gate
	// has never actually fired. RequestIsAdmin reads AuthDB() and keeps
	// the no-users deployment open.
	return !RequestIsAdmin(r)
}

// adminGated wraps a handler so only admins reach it. Single-user / auth-
// disabled deployments (no admin concept) pass through, matching WebRestricted,
// so a solo operator's own deployment still works.
func (T *PromptsApp) adminGated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !RequestIsAdmin(r) {
			http.Error(w, "Prompts is admin-only: editing framework prompt blocks changes what every agent receives on its next turn.", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (T *PromptsApp) Routes() {
	T.HandleFunc("/", T.adminGated(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	}))
	T.HandleFunc("/api/list", T.adminGated(T.handleList))     // GET  -> [{ID, Subject, Date}]
	T.HandleFunc("/api/load", T.adminGated(T.handleLoad))     // GET  ?id= -> {ID, Subject, Body, Date}
	T.HandleFunc("/api/save", T.adminGated(T.handleSave))     // POST {ID, Body}
	T.HandleFunc("/api/revert", T.adminGated(T.handleRevert)) // POST ?id=
	T.HandleFunc("/api/chat", T.adminGated(T.handleChat))     // POST {subject, body, message, mode, history}
	T.HandleFunc("/api/rules", T.adminGated(func(w http.ResponseWriter, r *http.Request) {
		HandleDocRules(w, r, T.DB, "prompts")
	}))
	T.HandleFunc("/api/assist", T.adminGated(T.handleAssist))      // POST {name, section, message, draft, history}
	T.HandleFunc("/api/revisions", T.adminGated(T.handleRevList))  // GET  ?id= -> [{id, date}]
	T.HandleFunc("/api/revision", T.adminGated(T.handleRevLoad))   // GET  ?revid= -> {body}
	T.HandleFunc("/api/read_back", T.adminGated(T.handleReadBack)) // POST {id, body, variant, tier} -> {model, wording, reading}
}

// lookupBlock finds a registered block by key — the guard that keeps the write
// endpoints scoped to real blocks (no arbitrary WebTable writes).
func lookupBlock(key string) (PromptBlock, bool) {
	for _, b := range AllPromptBlocks() {
		if b.Key == key {
			return b, true
		}
	}
	return PromptBlock{}, false
}

// lookupBlockByKeyOrTitle resolves a block from whichever identifier the
// caller has. The list endpoint hands the UI {ID: key, Subject: title},
// and the editor's title input holds the title — so an assist request
// arrives naming the block the way a human reads it, not the way the
// registry keys it.
func lookupBlockByKeyOrTitle(s string) (PromptBlock, bool) {
	if s == "" {
		return PromptBlock{}, false
	}
	if b, ok := lookupBlock(s); ok {
		return b, true
	}
	for _, b := range AllPromptBlocks() {
		if strings.EqualFold(strings.TrimSpace(b.Title), s) {
			return b, true
		}
	}
	return PromptBlock{}, false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- page --------------------------------------------------------------------

func (T *PromptsApp) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     EditorTitle,
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%", // editor-centric, fill the viewport
		// No hub Nav: Prompts lives in the admin UI now (RegisterAdminSection),
		// not the agent hub — this standalone page is reachable directly but
		// isn't a hub member. BackURL is enough.
		ExtraHeadHTML: promptsHeadHTML(),
		Sections: []ui.Section{
			{NoChrome: true, Body: promptsEditor()},
		},
	}
	page.ServeHTTP(w, r)
}

// promptsEditor is the workbench (list - editor - chat) over the prompt blocks.
// URLs are ABSOLUTE (/prompts/...) so it renders identically on the standalone
// /prompts page AND embedded in the admin page, which serves from a different
// path (relative "api/..." would resolve against /admin there).
func promptsEditor() ui.ArticleEditor {
	ed := promptsEditorBase()
	for _, a := range registeredEditorActions() {
		if a.Action.Label != "" {
			ed.Actions = append(ed.Actions, a.Action)
		}
	}
	ed.Actions = append(ed.Actions, ui.ToolbarAction{Label: "Revert to default", Title: "Discard the override and restore the shipped default text",
		Method: "client", URL: "prompts_revert"})
	return ed
}

func promptsEditorBase() ui.ArticleEditor {
	return ui.ArticleEditor{
		ListURL:          "/prompts/api/list",
		LoadURL:          "/prompts/api/load?id={id}",
		SaveURL:          "/prompts/api/save",
		ChatURL:          "/prompts/api/chat",
		RevisionsListURL: "/prompts/api/revisions?id={id}",
		RevisionLoadURL:  "/prompts/api/revision?revid={revid}",
		IDField:          "ID",
		SubjectField:     "Subject",
		BodyField:        "Body",
		DateField:        "Date",
		ListLabel:        "Prompts", // the block list is a fixed set — not "Articles"
		NoNew:            true,      // prompt blocks are framework-defined; you edit, not create
		NoSearch:         true,      // small fixed list — search is noise
		NoCollapse:       true,      // the list IS the page here; hiding it buys nothing
		TitleReadOnly:    true,      // a block's name is its key — edit the body, not the name
		// Each block in three versions: what both models read, and each
		// model's own wording (tier_text.go).
		Variants:         editorVariants(),
		VariantNoteField: "variant_note",
		EmptyText:        "Select a prompt block on the left to view and edit it.",
		PlaceholderTitle: "Block name",
		PlaceholderBody:  "The block's effective text: edit to override the shipped default; clear (or match the default) to revert.",
		// A prompt block IS structured markdown, so the outline is the
		// natural view of it. No Templates: blocks are framework-defined
		// and you edit them, never create one from a skeleton.
		Outline:   true,
		AssistURL: "/prompts/api/assist",
		RulesURL:  "/prompts/api/rules",
		Actions: []ui.ToolbarAction{
			{Label: "Tighten", Title: "Let the model make this block more concise and accurate, keeping every distinct instruction and lesson. Not measured against builds (Optimize on this tab is); the original is kept as a revision, so you can revert.",
				Method: "client", URL: "prompts_tighten"},
			{Label: "Save for both", Title: "Save this text for both models: what the worker and the lead both read, replacing each one's own wording of this block.",
				Method: "client", URL: "prompts_save_both"},
			{Label: "Check", Title: "What the worker and the lead take this block to mean, side by side (for a tool, when they would reach for it), each reading the wording it would be sent.",
				Method: "client", URL: "prompts_check"},
		},
	}
}

// promptsAdminSection wraps the editor as a full-width section on the admin
// LLMs tab, after Optimize (see the section source in init).
func promptsAdminSection() ui.Section {
	return ui.Section{
		Group:    AdminTab,
		Title:    EditorTitle,
		Subtitle: "Hand edits to the prompt blocks. Each tab shows what that model reads; Save changes that model only, Save for both changes both. A block marked worker or lead has wording of its own for that model, from Optimize or a hand edit.",
		Wide:     true,
		Body:     promptsEditor(),
	}
}

// --- list / load / save / revert ---------------------------------------------

func (T *PromptsApp) handleList(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// Rules, both the Always and the Style list, are not here: they are the
	// operator directing every agent, edited under Admin, Governance, Rules.
	out := []map[string]any{}
	for _, b := range AllPromptBlocks() {
		subject := b.Title
		date := b.Category
		// Mark an overridden block with an icon rather than the word "edited":
		// ✨ if its current text came from Tighten, ✎ if it was hand-edited.
		if _, overridden := PromptOverride(b.Key); overridden {
			if via := T.latestRevisionVia(b.Key); via == "tighten" || via == "optimize" {
				subject += "  ✨"
			} else {
				subject += "  ✎"
			}
		}
		row := map[string]any{"ID": b.Key, "Subject": subject, "Date": date}
		// Which models read wording of their own here: the split Optimize (or
		// a hand edit to one model's version) made, visible in the list.
		var own []string
		for _, t := range []string{prompts.TierWorker, prompts.TierLead} {
			if _, ok := prompts.PromptTierOverride(t, b.Key); ok {
				own = append(own, t)
			}
		}
		if len(own) > 0 {
			row["Badges"] = own
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

func (T *PromptsApp) handleLoad(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("id"))
	b, ok := lookupBlock(key)
	if !ok {
		http.Error(w, "unknown block", http.StatusNotFound)
		return
	}
	body, note := variantText(b, r.URL.Query().Get("variant"))
	writeJSON(w, map[string]any{
		"ID":           b.Key,
		"Subject":      b.Title,
		"Body":         body,
		"Date":         b.Category + " - Gate: " + b.Gate,
		"variant_note": note,
	})
}

func (T *PromptsApp) handleSave(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	var rec struct {
		ID   string `json:"ID"`
		Body string `json:"Body"`
		Via  string `json:"Via"` // "tighten" from Tighten; else a manual edit
		// Variant is the version saved: "worker" or "lead" for that model's
		// own wording, else the shared text.
		Variant string `json:"variant"`
		// Both saves the text for both models: the shared text, with each
		// model's own wording of the block dropped (Save for both).
		Both bool `json:"both"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	b, ok := lookupBlock(strings.TrimSpace(rec.ID))
	if !ok {
		http.Error(w, "unknown block", http.StatusNotFound)
		return
	}
	if rec.Both {
		T.saveBoth(b, rec.Body)
		writeJSON(w, map[string]any{"ok": true, "ID": b.Key})
		return
	}
	if v := strings.TrimSpace(rec.Variant); v == prompts.TierWorker || v == prompts.TierLead {
		if err := T.saveVariant(b, v, rec.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "ID": b.Key})
		return
	}
	via := "edit"
	if v := strings.TrimSpace(rec.Via); v == "tighten" || v == "optimize" {
		via = "tighten"
	}
	T.applyEdit(b, rec.Body, via, "")
	writeJSON(w, map[string]any{"ok": true, "ID": b.Key})
}

// applyEdit sets a block's live text, snapshotting what it replaced so the
// change can be undone. The one way a block changes, so a save here and a
// promotion from elsewhere leave the same history.
func (T *PromptsApp) applyEdit(b PromptBlock, body, via, note string) {
	// Snapshot the PRE-edit text as a revision so this change is reversible —
	// but only when it actually changes the effective text (no redundant snaps).
	prior := EffectivePromptText(b.Key, b.Text)
	clearing := strings.TrimSpace(body) == "" || body == b.Text
	newEffective := b.Text
	if !clearing {
		newEffective = body
	}
	if newEffective != prior {
		T.snapshotRevision(b.Key, prior, via, note)
	}
	// Blank or identical-to-default edits clear the override rather than
	// persisting a redundant copy — so "edited back to the default" reverts.
	if clearing {
		ClearPromptOverride(b.Key)
	} else {
		SetPromptOverride(b.Key, body)
	}
}

// ApplyBlockEdit changes a block's live text from outside this app, through
// the same path a save here takes: the replaced text becomes a revision on
// this page, revertible like any other. via labels it ("tuned" for a
// promotion from the tuning harness) and note says where it came from.
// Empty body, or the shipped text, clears the block back to its default.
func ApplyBlockEdit(key, body, via, note string) error {
	b, ok := lookupBlock(strings.TrimSpace(key))
	if !ok {
		return fmt.Errorf("no prompt block %q", key)
	}
	for _, a := range RegisteredApps() {
		if p, ok := a.(*PromptsApp); ok {
			p.applyEdit(b, body, via, note)
			return nil
		}
	}
	return fmt.Errorf("the prompts app is not running")
}

// --- revisions ---------------------------------------------------------------

const promptRevTable = "prompt_revisions"

// TunablePromptRevisionCap bounds the per-block undo history. Same reasoning as
// the guides revision cap: how far back an operator can reach is a deployment
// decision, and the shipped 10 is tight for anyone who runs Optimize often.
const TunablePromptRevisionCap = "tune_prompt_revision_cap"

func init() {
	RegisterTunable(TunableSpec{
		Key: TunablePromptRevisionCap, Category: "Limits", App: "/prompts",
		Label: "Prompt revision history",
		Help: "How many past versions of each prompt block are kept. Every edit and every Optimize " +
			"snapshots the text it replaced, so this is how many rewrites back you can reach to " +
			"restore one: the reason to keep revisions at all. Snapshots are single blocks of " +
			"text, so the storage is cheap and raising this is close to free. Lowering it prunes " +
			"on the next edit to each block, and the dropped snapshots are gone.",
		Kind: KindInt, Default: 10, Min: 2, Max: 200})
}

// maxRevisionsPerBlock is the effective cap, read per snapshot so an admin
// change applies to the next edit rather than waiting for a restart.
func maxRevisionsPerBlock() int { return TuneInt(TunablePromptRevisionCap) }

// promptRevision is one snapshot of a block's text just before an edit replaced
// it. Deployment-level (T.DB), matching the overrides they mirror.
type promptRevision struct {
	Block string `json:"block"`
	Date  string `json:"date"`
	Body  string `json:"body"`
	// Via records HOW the change that superseded this snapshot was made —
	// "edit" (a manual save), "tighten" (the model rewrite; older revisions
	// say "optimize") or "tuned" (a
	// variant promoted from the tuning harness). Lets the revision navigator
	// flag which snapshot is the one to restore, the main reason to keep
	// revisions at all.
	Via string `json:"via,omitempty"`
	// Note says where a change came from when that is not obvious: for a
	// tuned one, the variant and the score that earned it.
	Note string `json:"note,omitempty"`
}

// snapshotRevision stores `text` as a revision of blockKey and prunes to the
// most recent (per the operator's revision-history setting). revID is a
// nanosecond timestamp (URL-safe
// digits), so lexical order == chronological order.
func (T *PromptsApp) snapshotRevision(blockKey, text, via, note string) {
	if T.DB == nil {
		return
	}
	revID := strconv.FormatInt(time.Now().UnixNano(), 10)
	T.DB.Set(promptRevTable, revID, promptRevision{
		Block: blockKey,
		Date:  time.Now().Format("2006-01-02 15:04"),
		Body:  text,
		Via:   via,
		Note:  note,
	})
	var ids []string
	for _, k := range T.DB.Keys(promptRevTable) {
		var rev promptRevision
		if T.DB.Get(promptRevTable, k, &rev) && rev.Block == blockKey {
			ids = append(ids, k)
		}
	}
	sort.Strings(ids) // oldest first
	keep := maxRevisionsPerBlock()
	for len(ids) > keep {
		T.DB.Unset(promptRevTable, ids[0])
		ids = ids[1:]
	}
}

// latestRevisionVia reports how a block's CURRENT override was produced —
// "tighten" (or the older "optimize") or "edit". Every change snapshots the PRE-change text tagged with
// the action that replaced it, so the NEWEST revision's Via describes the text
// live now. "" when the block has no revisions. revID is a monotonic nanosecond
// timestamp, so the lexically-greatest key is the newest.
func (T *PromptsApp) latestRevisionVia(blockKey string) string {
	if T.DB == nil {
		return ""
	}
	newestID, via := "", ""
	for _, k := range T.DB.Keys(promptRevTable) {
		if k <= newestID {
			continue
		}
		var rev promptRevision
		if T.DB.Get(promptRevTable, k, &rev) && rev.Block == blockKey {
			newestID, via = k, rev.Via
		}
	}
	return via
}

func (T *PromptsApp) handleRevList(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	blockKey := strings.TrimSpace(r.URL.Query().Get("id"))
	out := make([]map[string]any, 0)
	if T.DB != nil {
		var ids []string
		byID := map[string]promptRevision{}
		for _, k := range T.DB.Keys(promptRevTable) {
			var rev promptRevision
			if T.DB.Get(promptRevTable, k, &rev) && rev.Block == blockKey {
				ids = append(ids, k)
				byID[k] = rev
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(ids))) // newest first
		for _, k := range ids {
			label := "edited"
			switch byID[k].Via {
			case "tighten", "optimize": // "optimize" is what Tighten was called before
				label = "tightened"
			case "tuned":
				label = "tuned"
				if n := strings.TrimSpace(byID[k].Note); n != "" {
					label += ": " + n
				}
			}
			out = append(out, map[string]any{"id": k, "date": byID[k].Date, "label": label})
		}
	}
	writeJSON(w, out)
}

func (T *PromptsApp) handleRevLoad(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	revID := strings.TrimSpace(r.URL.Query().Get("revid"))
	var rev promptRevision
	if T.DB == nil || !T.DB.Get(promptRevTable, revID, &rev) {
		http.Error(w, "revision not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"body": rev.Body})
}

func (T *PromptsApp) handleRevert(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("id"))
	if _, ok := lookupBlock(key); !ok {
		http.Error(w, "unknown block", http.StatusNotFound)
		return
	}
	b, _ := lookupBlock(key)
	T.saveBoth(b, "")
	writeJSON(w, map[string]any{"ok": true})
}

// saveBoth makes body what both models read: the shared text, and each
// model's own wording of the block dropped, each change a revision. Empty
// body puts both back on the shipped text (Revert to default).
func (T *PromptsApp) saveBoth(b PromptBlock, body string) {
	for _, tier := range []string{prompts.TierWorker, prompts.TierLead} {
		if o, ok := prompts.PromptTierOverride(tier, b.Key); ok {
			T.snapshotRevision(b.Key, o.Text, "edit", "the "+tier+"'s own wording, before both were set alike, "+time.Now().Format("Jan 2 15:04"))
			prompts.ClearPromptTierOverride(tier, b.Key)
		}
	}
	T.applyEdit(b, body, "edit", "")
}

// --- chat --------------------------------------------------------------------

func (T *PromptsApp) handleChat(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Message string `json:"message"`
		Mode    string `json:"mode"` // "chat" = discuss; anything else = propose a rewrite
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	editMode := req.Mode != "chat"

	sys := "You help an operator review and refine a gohort FRAMEWORK PROMPT BLOCK: text the system injects into agents' system prompts to shape their behavior. Be precise and terse. Preserve the block's intent and any hard-won \"this burned us\" lessons; don't add fluff, hedging, or AI-tells."
	if editMode {
		sys += " EDIT MODE: return ONLY the revised block text, no preamble, no explanation, no code fences."
	} else {
		sys += " DISCUSSION MODE: answer in conversational prose. Do NOT return a rewritten block; if the user wants a change applied, tell them to switch to Edit."
	}

	msgs := []Message{{Role: "system", Content: sys}}
	for _, h := range req.History {
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		role := h.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, Message{Role: role, Content: h.Content})
	}
	ctx := "The block being edited"
	if s := strings.TrimSpace(req.Subject); s != "" {
		ctx += " (" + s + ")"
	}
	msgs = append(msgs, Message{Role: "user", Content: ctx + ":\n```\n" + req.Body + "\n```\n\n" + req.Message})

	resp, err := T.WorkerChat(r.Context(), msgs)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	reply := strings.TrimSpace(resp.Content)
	if editMode {
		writeJSON(w, map[string]any{"type": "article", "content": reply})
	} else {
		writeJSON(w, map[string]any{"content": reply})
	}
}

// promptsHead registers the editor's toolbar actions. Tighten runs the worker
// model on the current block in edit mode with a canned "more concise and
// accurate" brief and applies the rewrite; ed.save() persists the text first,
// so the snapshot-on-save keeps it as a revision, one click to revert. App-specific behavior injected via
// ExtraHeadHTML per the core/ui domain-agnostic rule.
// promptsHeadHTML is the page head for both surfaces this editor appears
// on: the standalone /prompts page and the admin LLMs tab.
//
// It carries the shared inline-diff helper (core/editor) alongside this
// app's client actions. The editor proposes rewrites in chat-edit mode,
// and without these statics it fell back to an in-chat Approve/Deny
// list — a second, lesser diff that existed only because this app
// hadn't opted in. TechWriter already loaded them; now both surfaces
// review a proposal the same way.
func promptsHeadHTML() string {
	return "<style>" + editor.DiffCSS() + "</style>" +
		"<script>" + editor.UtilsJS() + editor.DiffJS() + "</script>" +
		promptsHead + editorActionHeads()
}

func editorActionHeads() string {
	var b strings.Builder
	for _, a := range registeredEditorActions() {
		b.WriteString(a.Head)
	}
	return b.String()
}

const promptsHead = `<script>
(function(){
  function register() {
  // The runtime defines uiRegisterClientAction; this head script can run before
  // it does, depending on where the host page injects section HTML. Returning
  // here used to end it: nothing registered, and the only symptom was a
  // "No handler for client action" toast at click time, long after the cause.
  // Wait for the registry instead of giving up on it.
  if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
  if (window.__promptsActionsRegistered) return;
  window.__promptsActionsRegistered = true;
  window.uiRegisterClientAction('prompts_tighten', function(ctx) {
    var ed = ctx.editor;
    if (!ed.getBody().trim()) { ed.toast('Select a block first'); return; }
    if (!ed.confirm('Tighten this block? The current text is kept as a revision, then replaced with a tighter version the model writes. Nothing checks it against builds; the revisions panel reverts it.')) return;
    ed.save(); // persist current text; once the rewrite saves, this becomes the revert point
    ed.busy(ctx.button, 'Tightening...');
    fetch('/prompts/api/chat', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        subject: ed.getTitle(),
        body: ed.getBody(),
        mode: 'edit',
        history: [],
        message: 'Make this more concise and accurate. Remove redundancy and filler, but preserve EVERY distinct instruction and every hard-won lesson; do not add hedging or AI-tells. Return only the revised block text.'
      })
    }).then(function(r){ return r.json(); }).then(function(d) {
      ed.restore(ctx.button);
      if (!d) { ed.toast('Empty response'); return; }
      if (d.error) { ed.toast('Error: ' + d.error); return; }
      if (d.content) {
        ed.setBody(d.content);
        ed.save({via:'tighten'}); // tags the pre-tighten snapshot as "tightened"
        ed.reloadList();
        ed.toast('Tightened. The original is kept as a revision.');
      } else {
        ed.toast('No rewrite produced');
      }
    }).catch(function(err){ ed.restore(ctx.button); ed.toast('Error: ' + (err && err.message || err)); });
  });
  // Save for both: the open text becomes what both models read.
  window.uiRegisterClientAction('prompts_save_both', function(ctx) {
    var ed = ctx.editor;
    if (!ed.getID()) { ed.toast('Select a block first'); return; }
    if (!ed.confirm('Save this text for both models? Each model\'s own wording of this block is replaced by it, and kept as a revision.')) return;
    ed.save({both: true});
    ed.reloadList();
    ed.toast('Saved for both models.');
  });
  window.uiRegisterClientAction('prompts_revert', function(ctx) {
    var ed = ctx.editor;
    var id = ed.getID();
    if (!id) { ed.toast('Select a block first'); return; }
    if (!ed.confirm('Revert this block to the shipped text for both models? Every edit to it, and each model\'s own wording, is discarded; each is kept as a revision.')) return;
    ed.busy(ctx.button, 'Reverting...');
    fetch('/prompts/api/revert?id=' + encodeURIComponent(id), {method: 'POST'}).then(function(r){ return r.json(); }).then(function(d){
      if (d && d.error) { ed.restore(ctx.button); ed.toast('Error: ' + d.error); return; }
      return fetch('/prompts/api/load?id=' + encodeURIComponent(id) + '&variant=' + encodeURIComponent(ed.getVariant ? ed.getVariant() : '')).then(function(r){ return r.json(); }).then(function(rec){
        ed.restore(ctx.button);
        if (rec && typeof rec.Body === 'string') ed.setBody(rec.Body);
        ed.reloadList();
        ed.toast('Reverted to default.');
      });
    }).catch(function(err){ ed.restore(ctx.button); ed.toast('Error: ' + (err && err.message || err)); });
  });
  }
  document.addEventListener('DOMContentLoaded', register);
  register();
})();
</script>`

// handleAssist powers the draft-with-me workbench over a prompt block:
// the text on one side, a conversation on the other. Same wire contract
// as TechWriter's and CodeWriter's, so the one shared workbench drives
// all three.
//
// Distinct from Tighten, which is a single unattended tightening pass.
// This is for the case where you want to talk about WHY a block says
// what it says before changing it.
func (T *PromptsApp) handleAssist(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.AppCore.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Section string `json:"section"`
		Message string `json:"message"`
		Draft   string `json:"draft"`
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	req.Section = strings.TrimSpace(req.Section)

	msgs := make([]Message, 0, len(req.History)+1)
	for _, t := range req.History {
		role := strings.TrimSpace(t.Role)
		if (role != "user" && role != "assistant") || strings.TrimSpace(t.Content) == "" {
			continue
		}
		msgs = append(msgs, Message{Role: role, Content: t.Content})
	}
	msgs = append(msgs, Message{Role: "user", Content: req.Message})

	// The block's shipped text and its gate are the reference material:
	// the question behind most edits is "how does this differ from what
	// the framework ships, and when does it even apply?"
	// The editor sends the block's DISPLAY title (that's what the title
	// input holds), not its key, so match on either.
	var reference string
	if b, ok := lookupBlockByKeyOrTitle(strings.TrimSpace(req.Name)); ok {
		reference = "Applies when: " + b.Gate + "\n\nShipped default:\n" + b.Text
	}

	var rules string
	if uid := AuthCurrentUser(r); uid != "" {
		rules = DocRulesSection(UserDB(T.DB, uid), "prompts")
	}
	resp, err := T.AppCore.WorkerChat(r.Context(), msgs,
		WithSystemPrompt(BuildDocAssistPrompt(req.Name, req.Section, req.Draft, reference)+rules),
		WithRouteKey("app.prompts.assist"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "assist failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	reply, value := SplitDraftReply(ResponseText(resp))
	if value != "" && req.Section != "" {
		value = StripLeadingHeading(value, req.Section)
	}
	writeJSON(w, map[string]any{"reply": reply, "value": value})
}
