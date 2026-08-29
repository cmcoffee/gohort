// Package prompts is the Prompts hub surface: a document-workbench (list ·
// editor · chat, via core/ui's ArticleEditor) over the framework prompt blocks
// that shape agent behavior — the "hidden prompts" made visible AND editable.
// The left list is the registered blocks; the centre editor holds a block's
// effective text; the right chat refines it with the worker LLM. Saving stores
// a deployment-level override that the prompt assembler reads, so an edit
// changes what agents receive on their next turn; saving the default (or an
// empty body) reverts. Blocks come from core's PromptBlock registry (see
// apps/orchestrate/framework_prompts_registry.go).
package prompts

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/editor"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterApp(new(PromptsApp))
	// Editing the framework prompt blocks is deployment tuning, not agent
	// behavior — so the editor lives inside the admin UI (a "Prompts" tab),
	// self-registered here rather than surfaced as an agent-facing hub app. The
	// app's routes below still serve the editor; WebHidden keeps it off the
	// dashboard and there's no HubTab, so it's reached only from admin.
	RegisterAdminSection(AdminSectionEntry{Section: promptsAdminSection(), Head: promptsHeadHTML()})
}

// PromptsApp is the app entry point. Embedding AppCore wires in shared state
// (DB, flag set) and the LLM handles the chat pane uses.
type PromptsApp struct {
	AppCore
	optimizeMu    sync.Mutex // guards the background Optimize-all run + its progress
	optimizing    bool
	optimizeDone  int
	optimizeTotal int
}

// --- core.Agent interface ----------------------------------------------------

// Pointer receivers, like every other app that holds non-copyable state
// (OrchestrateApp, FileStoreApp). PromptsApp gained optimizeMu when the
// Optimize-all background run landed, and these three kept the value receivers
// they were written with — so each call copied the struct, mutex included.
// go vet's copylocks flags it: copying a sync.Mutex yields a second lock that
// guards nothing, and a copy made while the original is held starts out locked.
// Harmless in these three (they read a constant and drop the copy) but the
// pattern is one edit away from being load-bearing, and RegisterApp already
// passes a pointer, so nothing outside had to change.
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
func (T *PromptsApp) WebName() string { return "Prompts" }
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
	T.HandleFunc("/api/style/form", T.adminGated(T.handleStyleForm))
	T.HandleFunc("/api/global", T.adminGated(T.handleGlobalRules)) // GET/POST the operator rule list
	T.HandleFunc("/api/global/form", T.adminGated(T.handleGlobalForm))
	T.HandleFunc("/api/style", T.adminGated(T.handleStyleRules))                   // GET -> {builtins, custom}; POST {disabled, custom}
	T.HandleFunc("/api/assist", T.adminGated(T.handleAssist))                      // POST {name, section, message, draft, history}
	T.HandleFunc("/api/revisions", T.adminGated(T.handleRevList))                  // GET  ?id= -> [{id, date}]
	T.HandleFunc("/api/revision", T.adminGated(T.handleRevLoad))                   // GET  ?revid= -> {body}
	T.HandleFunc("/api/optimize-all", T.adminGated(T.handleOptimizeAll))           // POST -> starts a background pass
	T.HandleFunc("/api/optimize-all/status", T.adminGated(T.handleOptimizeStatus)) // GET  -> {optimizing, done, total}
}

// styleRulesRowID is the synthetic list row that opens the style-rule editor.
// Not a block key: nothing loads or saves under it.
const styleRulesRowID = "__style_rules__"

const globalRulesRowID = "__global_rules__"

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
		Title:     "Prompts",
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

// promptsEditor is the workbench (list · editor · chat) over the prompt blocks.
// URLs are ABSOLUTE (/prompts/...) so it renders identically on the standalone
// /prompts page AND embedded in the admin page, which serves from a different
// path (relative "api/..." would resolve against /admin there).
func promptsEditor() ui.ArticleEditor {
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
		EmptyText:        "Select a prompt block on the left to view and edit it.",
		PlaceholderTitle: "Block name",
		PlaceholderBody:  "The block's effective text — edit to override the shipped default; clear (or match the default) to revert.",
		// A prompt block IS structured markdown, so the outline is the
		// natural view of it. No Templates: blocks are framework-defined
		// and you edit them, never create one from a skeleton.
		Outline:   true,
		AssistURL: "/prompts/api/assist",
		RulesURL:  "/prompts/api/rules",
		Actions: []ui.ToolbarAction{
			{Label: "Optimize", Title: "Let the model tighten this block — more concise and accurate, preserving every distinct instruction and lesson. The original is saved as a revision first, so you can revert.",
				Method: "client", URL: "prompts_optimize"},
			{Label: "Revert to default", Title: "Discard the override and restore the shipped default text",
				Method: "client", URL: "prompts_revert"},
		},
		// Whole-list action lives on the list header, not the per-block toolbar.
		ListActions: []ui.ToolbarAction{
			{Label: "Optimize all", Title: "Run the tighten pass over every block in the background. Each block's current text is saved as a revision first, so any block is revertible.",
				Method: "client", URL: "prompts_optimize_all"},
		},
	}
}

// promptsAdminSection wraps the editor as a full-width admin "Prompts" tab — the
// primary home for prompt tuning (see RegisterAdminSection in init).
func promptsAdminSection() ui.Section {
	return ui.Section{
		Group:    "Prompts",
		Wide:     true,
		NoChrome: true,
		Body:     promptsEditor(),
	}
}

// --- list / load / save / revert ---------------------------------------------

func (T *PromptsApp) handleList(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	// Style rules ride at the TOP of the list as a selectable row rather than a
	// button: they are prompt text like everything else here, so they belong in
	// the same list. The Action field routes the click to a list editor instead
	// of the prose pane, because a set of one-line rules is not a document.
	out := []map[string]any{{
		"ID": globalRulesRowID, "Subject": "Global rules", "Date": "Rules",
		"Action": "prompts_global_rules",
	}, {
		"ID": styleRulesRowID, "Subject": "Style rules", "Date": "Style",
		"Action": "prompts_style_rules",
	}}
	for _, b := range AllPromptBlocks() {
		subject := b.Title
		date := b.Category
		// Mark an overridden block with an icon rather than the word "edited":
		// ✨ if its current text came from an Optimize, ✎ if it was hand-edited.
		if _, overridden := PromptOverride(b.Key); overridden {
			if T.latestRevisionVia(b.Key) == "optimize" {
				subject += "  ✨"
			} else {
				subject += "  ✎"
			}
		}
		out = append(out, map[string]any{"ID": b.Key, "Subject": subject, "Date": date})
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
	writeJSON(w, map[string]any{
		"ID":      b.Key,
		"Subject": b.Title,
		"Body":    EffectivePromptText(key, b.Text),
		"Date":    b.Category + " · Gate: " + b.Gate,
	})
}

func (T *PromptsApp) handleSave(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	var rec struct {
		ID   string `json:"ID"`
		Body string `json:"Body"`
		Via  string `json:"Via"` // "optimize" from the Optimize action; else a manual edit
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
	// Snapshot the PRE-edit text as a revision so this change is reversible —
	// but only when it actually changes the effective text (no redundant snaps).
	prior := EffectivePromptText(b.Key, b.Text)
	clearing := strings.TrimSpace(rec.Body) == "" || rec.Body == b.Text
	newEffective := b.Text
	if !clearing {
		newEffective = rec.Body
	}
	if newEffective != prior {
		via := "edit"
		if strings.TrimSpace(rec.Via) == "optimize" {
			via = "optimize"
		}
		T.snapshotRevision(b.Key, prior, via)
	}
	// Blank or identical-to-default edits clear the override rather than
	// persisting a redundant copy — so "edited back to the default" reverts.
	if clearing {
		ClearPromptOverride(b.Key)
	} else {
		SetPromptOverride(b.Key, rec.Body)
	}
	writeJSON(w, map[string]any{"ok": true, "ID": b.Key})
}

// --- revisions ---------------------------------------------------------------

const promptRevTable = "prompt_revisions"
const maxRevisionsPerBlock = 10

// promptRevision is one snapshot of a block's text just before an edit replaced
// it. Deployment-level (T.DB), matching the overrides they mirror.
type promptRevision struct {
	Block string `json:"block"`
	Date  string `json:"date"`
	Body  string `json:"body"`
	// Via records HOW the change that superseded this snapshot was made —
	// "edit" (a manual save) or "optimize" (the model rewrite). Lets the
	// revision navigator flag which snapshot is the pre-Optimize one to
	// restore, the main reason to keep revisions at all.
	Via string `json:"via,omitempty"`
}

// snapshotRevision stores `text` as a revision of blockKey and prunes to the
// most recent maxRevisionsPerBlock. revID is a nanosecond timestamp (URL-safe
// digits), so lexical order == chronological order.
func (T *PromptsApp) snapshotRevision(blockKey, text, via string) {
	if T.DB == nil {
		return
	}
	revID := strconv.FormatInt(time.Now().UnixNano(), 10)
	T.DB.Set(promptRevTable, revID, promptRevision{
		Block: blockKey,
		Date:  time.Now().Format("2006-01-02 15:04"),
		Body:  text,
		Via:   via,
	})
	var ids []string
	for _, k := range T.DB.Keys(promptRevTable) {
		var rev promptRevision
		if T.DB.Get(promptRevTable, k, &rev) && rev.Block == blockKey {
			ids = append(ids, k)
		}
	}
	sort.Strings(ids) // oldest first
	for len(ids) > maxRevisionsPerBlock {
		T.DB.Unset(promptRevTable, ids[0])
		ids = ids[1:]
	}
}

// latestRevisionVia reports how a block's CURRENT override was produced —
// "optimize" or "edit". Every change snapshots the PRE-change text tagged with
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
			if byID[k].Via == "optimize" {
				label = "optimized"
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
	ClearPromptOverride(key)
	writeJSON(w, map[string]any{"ok": true})
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

	sys := "You help an operator review and refine a gohort FRAMEWORK PROMPT BLOCK — text the system injects into agents' system prompts to shape their behavior. Be precise and terse. Preserve the block's intent and any hard-won \"this burned us\" lessons; don't add fluff, hedging, or AI-tells."
	if editMode {
		sys += " EDIT MODE: return ONLY the revised block text — no preamble, no explanation, no code fences."
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

// optimizeText runs the worker model on a block in edit mode with the canned
// "more concise and accurate" brief and returns the revised text. Shared by the
// single-block Optimize (via the chat endpoint) and the bulk pass below.
func (T *PromptsApp) optimizeText(ctx context.Context, title, current string) (string, error) {
	sys := "You refine a gohort FRAMEWORK PROMPT BLOCK — text injected into agents' system prompts to shape behavior. EDIT MODE: return ONLY the revised block text, no preamble or code fences. Be precise and terse; preserve every distinct instruction and every hard-won \"this burned us\" lesson; remove only redundancy and filler; no hedging or AI-tells."
	ctxLine := "The block"
	if s := strings.TrimSpace(title); s != "" {
		ctxLine += " (" + s + ")"
	}
	msgs := []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: ctxLine + ":\n```\n" + current + "\n```\n\nMake this more concise and accurate. Return only the revised block text."},
	}
	resp, err := T.WorkerChat(ctx, msgs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// handleOptimizeAll starts a background pass that optimizes every block. It runs
// in a goroutine because N sequential LLM calls exceed an HTTP timeout; the
// response returns immediately and the operator reloads to see results ("edited"
// badges) appear. A guard prevents overlapping runs. Each block's prior text is
// snapshotted as a revision first, so every result is individually revertible.
func (T *PromptsApp) handleOptimizeAll(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	blocks := AllPromptBlocks()
	T.optimizeMu.Lock()
	if T.optimizing {
		T.optimizeMu.Unlock()
		writeJSON(w, map[string]any{"error": "an optimize-all run is already in progress"})
		return
	}
	T.optimizing = true
	T.optimizeDone = 0
	T.optimizeTotal = len(blocks)
	T.optimizeMu.Unlock()

	go func() {
		defer func() {
			T.optimizeMu.Lock()
			T.optimizing = false
			T.optimizeMu.Unlock()
		}()
		ctx := context.Background()
		n := 0
		for _, b := range blocks {
			current := EffectivePromptText(b.Key, b.Text)
			out, err := T.optimizeText(ctx, b.Title, current)
			if err != nil {
				Log("[prompts] optimize-all: %s failed: %v", b.Key, err)
			} else if out != "" && out != current {
				T.snapshotRevision(b.Key, current, "optimize")
				if out == b.Text {
					ClearPromptOverride(b.Key)
				} else {
					SetPromptOverride(b.Key, out)
				}
				n++
			}
			T.optimizeMu.Lock()
			T.optimizeDone++
			T.optimizeMu.Unlock()
		}
		Log("[prompts] optimize-all: %d/%d blocks changed", n, len(blocks))
	}()
	writeJSON(w, map[string]any{"started": true, "total": len(blocks)})
}

// handleOptimizeStatus reports the background pass's progress so the client can
// show live "N/M" feedback and refresh the list as blocks complete.
func (T *PromptsApp) handleOptimizeStatus(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.optimizeMu.Lock()
	resp := map[string]any{"optimizing": T.optimizing, "done": T.optimizeDone, "total": T.optimizeTotal}
	T.optimizeMu.Unlock()
	writeJSON(w, resp)
}

// promptsHead registers the "Optimize" toolbar action: it runs the worker model
// on the current block in edit mode with a canned "more concise and accurate"
// brief and applies the rewrite. ed.save() persists the pre-optimize text first,
// so the snapshot-on-save captures it as a revision — a bad optimization is one
// click to revert in the revisions panel. App-specific behavior injected via
// ExtraHeadHTML per the core/ui domain-agnostic rule.
// promptsHeadHTML is the page head for both surfaces this editor appears
// on: the standalone /prompts page and the admin "Prompts" tab.
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
		promptsHead
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
  window.uiRegisterClientAction('prompts_optimize', function(ctx) {
    var ed = ctx.editor;
    if (!ed.getBody().trim()) { ed.toast('Select a block first'); return; }
    if (!ed.confirm('Optimize this block? The current text is saved as a revision first, then replaced with a tighter version the model produces. Use the revisions panel to revert.')) return;
    ed.save(); // persist current text; once the rewrite saves, this becomes the revert point
    ed.busy(ctx.button, 'Optimizing...');
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
        ed.save({via:'optimize'}); // optimized result; tags the pre-optimize snapshot as "optimized"
        ed.reloadList();
        ed.toast('Optimized. Original saved as a revision.');
      } else {
        ed.toast('No rewrite produced');
      }
    }).catch(function(err){ ed.restore(ctx.button); ed.toast('Error: ' + (err && err.message || err)); });
  });
  // Style rules open a LIST editor, not the workbench pane. A one-line rule is
  // a checkbox or a line in a textarea; loading it into a full prose editor
  // with a chat pane beside it would be the wrong instrument for six words.
  //
  // Shipped rules get a checkbox rather than an edit box: switching one off is
  // reversible and leaves the text on screen to switch back on, where letting
  // it be rewritten would quietly lose the reason it exists. Rules marked
  // "enforced" also run as a transform on the way out, so unchecking one stops
  // both halves at once.
  // The Style rules row opens the rule editor page. It is a FormPanel over a
  // "rules" field, the framework's line-separated list editor, which is the same
  // control the persona editor uses. Building a bespoke modal here would mean a
  // second rule editor that behaves almost, but not quite, like the real one.
  function promptsOpenRuleEditor(ctx, title, formURL) {
    fetch(formURL).then(function(r){ return r.json(); }).then(function(spec) {
      var handle = window.uiOpenSimpleModal({
        title: title, width: 'min(720px, 94vw)',
        mount: function(body) {
          window.uiMountComponent(spec, body, {
            __closeModal: function(){ if (handle) handle.close(); },
          });
        },
      });
    }).catch(function(err){
      ctx.editor.toast('Could not open ' + title + ': ' + (err && err.message || err));
    });
  }
  window.uiRegisterClientAction('prompts_style_rules', function(ctx) {
    promptsOpenRuleEditor(ctx, 'Style rules', '/prompts/api/style/form');
  });
  window.uiRegisterClientAction('prompts_global_rules', function(ctx) {
    promptsOpenRuleEditor(ctx, 'Global rules', '/prompts/api/global/form');
  });
  window.uiRegisterClientAction('prompts_optimize_all', function(ctx) {
    var ed = ctx.editor;
    if (!ed.confirm('Optimize ALL blocks? Each block current text is saved as a revision first, then replaced with a tighter version. This runs in the background and may take a minute; revert any block from its revisions panel.')) return;
    fetch('/prompts/api/optimize-all', {method: 'POST'}).then(function(r){ return r.json(); }).then(function(d){
      if (d && d.error) { ed.toast('Error: ' + d.error); return; }
      ed.toast('Optimizing all ' + (d.total || '') + ' blocks - badges update as each finishes...');
      var poll = function() {
        fetch('/prompts/api/optimize-all/status').then(function(r){ return r.json(); }).then(function(s){
          ed.reloadList();
          if (s && s.optimizing) {
            ed.toast('Optimizing ' + (s.done || 0) + '/' + (s.total || 0) + '...');
            setTimeout(poll, 3000);
          } else {
            ed.toast('Optimize all complete (' + (s && s.done || 0) + '/' + (s && s.total || 0) + ' processed).');
          }
        }).catch(function(){ setTimeout(poll, 3000); });
      };
      setTimeout(poll, 3000);
    }).catch(function(err){ ed.toast('Error: ' + (err && err.message || err)); });
  });
  window.uiRegisterClientAction('prompts_revert', function(ctx) {
    var ed = ctx.editor;
    var id = ed.getID();
    if (!id) { ed.toast('Select a block first'); return; }
    if (!ed.confirm('Revert this block to the shipped default? Your override is discarded.')) return;
    ed.busy(ctx.button, 'Reverting...');
    fetch('/prompts/api/revert?id=' + encodeURIComponent(id), {method: 'POST'}).then(function(r){ return r.json(); }).then(function(d){
      if (d && d.error) { ed.restore(ctx.button); ed.toast('Error: ' + d.error); return; }
      return fetch('/prompts/api/load?id=' + encodeURIComponent(id)).then(function(r){ return r.json(); }).then(function(rec){
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
// Distinct from Optimize, which is a single unattended tightening pass.
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
