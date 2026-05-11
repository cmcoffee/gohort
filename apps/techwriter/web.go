package techwriter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/editor"
	"github.com/cmcoffee/gohort/core/webui"
)


const promptDocumentation = `You are a technical writing assistant helping create documentation, runbooks, and instruction articles for IT teams.

When the user shares commands or partial instructions, expand them into clear step-by-step documentation. Preserve all commands exactly as given — wrap them in code blocks. Add context around commands: what they do, expected output, and potential errors.

When asked to process the article, fill in missing context, expand bare commands, flesh out incomplete steps, and ensure the article flows well. Write in a professional but approachable tone suitable for a Confluence wiki. Use numbered steps for procedures, bullet points for lists, and code blocks for commands.

FORMATTING RULES:
- Important notices, warnings, and prerequisites MUST be on their own line, set apart from the surrounding text. Use bold or a blockquote to make them visually distinct:
  > **WARNING:** This will delete all data in the database.
  > **NOTE:** Requires admin access to the production cluster.
  > **IMPORTANT:** Back up the configuration before proceeding.
- Do NOT bury warnings inside a paragraph of other text. If something can cause data loss, break a system, or requires elevated access, it gets its own line.
- Keep sentences concise. If a sentence runs longer than ~120 characters, break it into two sentences or restructure as a list. Long sentences in technical docs cause readers to miss critical details.
- One idea per sentence. Do not chain multiple instructions with "and" or "then" in a single sentence.
- Use line breaks between distinct steps or concepts. Dense paragraphs are harder to scan than spaced-out steps.
- SECTION HEADINGS: Number every ## heading. Format is "## N. Section Title" — for example "## 1. Setup", "## 2. Configuration", "## 3. Deployment Steps". Start at 1 and increment by 1. Sub-sections (### …) and special sections (## Sources, ## References) are NOT numbered.
- TABLE OF CONTENTS: After the introductory paragraph (and before "## 1. ..."), write a "## Table of Contents" section as a bulleted list of markdown anchor links — one bullet per numbered ## heading. Use the slug rules below. Do NOT include sub-sections or special sections (Sources, References).
- ANCHOR SLUG ALGORITHM (standard GitHub-flavored markdown — use this exactly for both the TOC and any cross-reference link). Given the FULL heading text after "## ":
  1. Lowercase.
  2. Replace each space with a single dash.
  3. Drop every character that is NOT a-z, 0-9, dash, or underscore.
  4. Do NOT collapse consecutive dashes. Do NOT trim leading/trailing dashes.
  Worked examples:
    "1. Setup"                       → slug: 1-setup
    "2. Distribution & Version"      → slug: 2-distribution--version    (yes, double dash — space-amp-space)
    "3. API Keys (sensitive)"        → slug: 3-api-keys-sensitive
    "4. Configuration: Secrets"      → slug: 4-configuration-secrets
  TOC entry: "- [1. Setup](#1-setup)" / "- [2. Distribution & Version](#2-distribution--version)"
- IN-PROSE CROSS-REFERENCES: link by full heading text using the same slug: "See [4. Rollback](#4-rollback) if something goes wrong." Do not say "see below" or "see the section above" — link directly by section title.

IMPORTANT: If the article contains source citations like [1], [2], [N] or a ## Sources section, preserve ALL citations exactly as they appear. Keep every [N] reference in the text and keep the Sources section intact.

ASCII DIAGRAMS — straighten any lines that drift. When the article contains an ASCII diagram (architecture, flow, network topology, etc.), inspect every line of it BEFORE returning your output and fix any line that deviates without reason:
- Vertical lines (|) must hold the same column from top to bottom. If a "|" character shifts left or right between rows for no semantic reason (it's not a branch, not a corner), realign it to the column its neighbors use.
- Horizontal lines (-, =) must be continuous within their span. No stray gaps in the middle of a line, no extra dashes that overshoot a corner.
- Corner / junction characters (+, T, └, ┘, ├, ┤, etc.) must sit at the actual intersection. A "+" floating one column off where the lines meet is a defect — move it.
- Box widths must be consistent. If a box has 20 dashes on its top edge and 18 on its bottom, the bottom is wrong (or the top is) — pad whichever is shorter.
- Whitespace inside a box must not change column counts mid-flow. Padding spaces should be uniform so labels left-align with their box edges.
Treat a wobbly diagram as a bug. The diagram represents a precise relationship; if the rendering doesn't, fix it. Use a fixed-width font mental model — every character is one column, every row is one line. Apply the diagram fix as part of normal "process this article" / rewrite flows; do not announce it, just deliver clean output.

RENDERER CAPABILITIES (what survives the markdown → HTML export):
The export pipeline supports a defined subset of markdown. Stick to it — anything outside this list will be rendered as plain text or stripped.

  Supported block elements:
    - Headings: # H1, ## H2, ### H3 (deeper levels are not styled)
    - Paragraphs (separated by a blank line)
    - Fenced code blocks: triple-backtick opens, triple-backtick closes (language tag is ignored — no syntax highlighting, but the fence is required for code formatting)
    - Bullet lists: lines starting with "- " or "* " (single level — nested bullets DO NOT render as nested; keep lists flat)
    - Numbered lists: "1. ", "2. ", … (single level only)
    - Blockquotes: lines starting with "> "
    - Horizontal rule: --- on its own line
    - Tables: pipe-delimited with a |---|---| separator row after the header. Cells may contain inline markdown.

  Supported inline elements:
    - **bold**, *italic*, ` + "`inline code`" + `, [link text](https://url)
    - Citation superscripts via raw HTML are passed through: <sup><a href="...">1</a></sup>

  NOT supported — avoid these, they will not render correctly:
    - Nested lists (sub-bullets indented under a parent bullet)
    - Task lists: - [ ] / - [x]
    - Images: ![alt](url)
    - Bare/auto-linked URLs (always use [text](url))
    - Strikethrough ~~text~~, footnotes, definition lists, admonition syntax
    - HTML beyond the <sup><a> citation pattern
    - Heading anchor IDs like {#custom-id}

When you produce article content (new or revised), output it as clean markdown that can be directly pasted into the article editor. Start your response with ARTICLE: on its own line if you're providing updated article content, followed by the full article markdown. If you're just answering a question or chatting, respond normally without the ARTICLE: prefix.
` + BannedWordsRule

const defaultPromptTable = "techwriter_default_prompt"

// activeDefaultPrompt returns the custom default prompt if set, otherwise the built-in.
func (T *TechWriterAgent) activeDefaultPrompt() string {
	if T.DB != nil {
		var custom string
		if T.DB.Get(defaultPromptTable, "prompt", &custom) && custom != "" {
			return custom
		}
	}
	return promptDocumentation
}

func (T *TechWriterAgent) handlePrompt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"prompt":     T.activeDefaultPrompt(),
			"is_default": T.activeDefaultPrompt() == promptDocumentation,
		})
	case http.MethodPost:
		var req struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" {
			http.Error(w, "prompt required", http.StatusBadRequest)
			return
		}
		if T.DB != nil {
			T.DB.Set(defaultPromptTable, "prompt", req.Prompt)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
	case http.MethodDelete:
		if T.DB != nil {
			T.DB.Unset(defaultPromptTable, "prompt")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "reverted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *TechWriterAgent) WebPath() string { return "/techwriter" }
func (T *TechWriterAgent) WebName() string { return "TechWriter" }

// WebRestricted gates the app behind per-user app permission. A user
// who isn't granted /techwriter in the admin panel gets a 404.
func (T *TechWriterAgent) WebRestricted(r *http.Request) bool {
	return !UserHasAppAccess(r, "/techwriter")
}

// WebAccessKey + WebAccessCheck expose a boolean at /api/access so
// other apps (research UI) can hide their "Push to TechWriter"
// controls from users without access. Mirrors WebRestricted.
func (T *TechWriterAgent) WebAccessKey() string                 { return "techwriter" }
func (T *TechWriterAgent) WebAccessCheck(r *http.Request) bool  { return UserHasAppAccess(r, "/techwriter") }
func (T *TechWriterAgent) WebDesc() string {
	return "Technical article co-writer for documentation and instructions"
}

func (T *TechWriterAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	// New framework-based techwriter at /techwriter/. Same backend
	// API endpoints as legacy — only the page chrome and main UI
	// changed.
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handleNewTechwriterPage(w, r)
	})
	// Legacy techwriter (full feature set: persona, revisions, merge,
	// export/import, image gen). Kept for parity until each feature
	// gets ported into the framework version above.
	sub.HandleFunc("/legacy", func(w http.ResponseWriter, r *http.Request) {
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "TechWriter (Legacy)",
			AppName:  "TechWriter",
			Prefix:   prefix,
			BodyHTML: techwriterBody,
			AppCSS:   editor.DiffCSS() + techwriterCSS,
			AppJS:    editor.UtilsJS() + editor.DiffJS() + techwriterJS,
		}))
	})
	sub.HandleFunc("/legacy/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/legacy", http.StatusMovedPermanently)
	})
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/save", T.handleSave)
	sub.HandleFunc("/api/list", T.handleList)
	sub.HandleFunc("/api/load/", T.handleLoad)
	sub.HandleFunc("/api/delete/", T.handleDelete)
	sub.HandleFunc("/api/export/", T.handleExport)
	sub.HandleFunc("/api/import", T.handleImport)
	sub.HandleFunc("/api/merge", T.handleMerge)
	sub.HandleFunc("/api/merge-sources", T.handleMergeSources)
	sub.HandleFunc("/api/merge-source/", T.handleMergeSource)
	sub.HandleFunc("/api/revisions/", T.handleRevisions)
	sub.HandleFunc("/api/revision/", T.handleRevision)
	sub.HandleFunc("/api/suggest-title", T.handleSuggestTitle)
	sub.HandleFunc("/api/prompt", T.handlePrompt)
	sub.HandleFunc("/api/preview", T.handlePreview)
	sub.HandleFunc("/api/upload-persona", T.handleUploadPersona)
	sub.HandleFunc("/api/save-persona", T.handleSavePersona)
	sub.HandleFunc("/api/personas", T.handleListPersonas)
	sub.HandleFunc("/api/persona/", T.handlePersona)
	sub.HandleFunc("/api/rules", T.handleRules)
	sub.HandleFunc("/api/generate-image", T.handleGenerateImage)

	RegisterUserDataHandler(&techWriterUserData{agent: T})

	twDB := T.DB
	SaveArticleFunc = func(userID, subject, body string) (string, error) {
		udb := UserDB(twDB, userID)
		id := UUIDv4()
		now := time.Now().Format(time.RFC3339)
		udb.Set(HistoryTable, id, ArticleRecord{
			ID:      id,
			Subject: subject,
			Body:    body,
			Date:    now,
		})
		revID := UUIDv4()
		udb.Set(revisionTable, revID, ArticleRevision{
			ID:        revID,
			ArticleID: id,
			Subject:   subject,
			Body:      body,
			Date:      now,
		})
		return id, nil
	}

	// Gate the entire sub-mux behind per-user app permission.
	// Loopback requests bypass the check so internal inter-app RPCs
	// aren't blocked by the server's own gate. Unauthorized external
	// requests get 404 to conceal the app's existence.
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsLoopbackRequest(r) && !UserHasAppAccess(r, "/techwriter") {
			http.NotFound(w, r)
			return
		}
		sub.ServeHTTP(w, r)
	})
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, gated))
	} else {
		mux.Handle("/", gated)
	}
}

func (T *TechWriterAgent) handleChat(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		Message  string `json:"message"`
		// Mode controls whether the LLM may propose an article rewrite.
		// "edit" (default) = legacy behavior; the LLM may emit an
		// ARTICLE:-prefixed response and the UI offers to apply it.
		// "chat" = discuss / explain / ask questions only; never emit
		// a full article body. Used for "talk about the draft before
		// touching it" conversations. Blank defaults to edit.
		Mode        string `json:"mode"`
		Persona     string `json:"persona"`      // selected persona name
		PersonaEdit bool   `json:"persona_edit"` // true when editing a persona in the editor
		// History is the prior conversation, client-maintained. Lets
		// Chat → Edit flow carry discussion context so Edit can act on
		// what was just discussed. Article state (body/subject)
		// attaches to the CURRENT message only, never embedded into
		// past turns — so the LLM always sees the current article,
		// not stale snapshots.
		History []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"history"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	chatOnly := req.Mode == "chat"

	// Strip ## Sources section — LLM should never rewrite it.
	// We'll reattach it to the article output after the LLM responds.
	body_for_llm, preserved_sources := SplitSourcesSection(req.Body)

	var article_context string
	if body_for_llm != "" {
		article_context = fmt.Sprintf("\n\nCurrent article (subject: %s):\n```\n%s\n```", req.Subject, body_for_llm)
	} else if req.Subject != "" {
		article_context = fmt.Sprintf("\n\nArticle subject: %s\n(Article body is empty — this is a new article.)", req.Subject)
	}

	// Compose system prompt: base default + persona style (legacy
	// path, kept for /techwriter/legacy) + per-user Rules. Rules go
	// LAST so they're closest to the user message and weigh heaviest
	// on the model's behavior.
	system_prompt := T.activeDefaultPrompt() +
		personaPromptSection(T.loadPersonaStyle(udb, req.Persona)) +
		loadUserRules(udb)
	if req.PersonaEdit {
		system_prompt = `You are helping the user edit a writing style persona (a set of rules for how to write). The content in the editor is a persona definition, not an article. When the user asks you to modify it (add rules, remove sections, change tone), apply the change and return the COMPLETE updated persona. Always prefix your output with ARTICLE: followed by the full updated persona text. Do not discuss the change — just make it and return the result.

When creating or editing a persona, ALWAYS ensure these anti-LLM rules are included in the persona (add them if missing):
- Zero em-dashes or double-hyphens. Rewrite as periods, commas, or new sentences.
- Banned words: "demonstrably", "underscores", "highlights", "reflects", "landscape" (as metaphor), "leverage" (as verb), "delve", "robust", "navigate", "it's worth noting", "in an era where", "paradigm shift", "game-changer".
- Banned transitions: "Meanwhile", "At the same time", "This dynamic creates".
- Use active voice. Short paragraphs. Name people and numbers, never "experts say".
These rules prevent the output from reading as AI-generated. They should be part of every persona.`
	}

	if chatOnly {
		// Discussion-only mode overrides any mode-specific rule above
		// (including the PersonaEdit ARTICLE: rule). The user is
		// talking about the draft, not asking for a rewrite, so the
		// LLM must not emit an article body under any label. Applied
		// last so it wins when multiple rule blocks conflict.
		system_prompt += "\n\nDISCUSSION MODE: the user is chatting about the article, not asking for it to be rewritten. Do NOT emit an ARTICLE: prefix. Do NOT return a revised or complete article body. Answer questions, explain your thinking, suggest approaches, or describe the change you'd make — all in conversational prose. If the user says \"make the change\" or similar, tell them to click Edit instead of Chat."
	}
	today := time.Now().Format("January 2, 2006")

	// Build message list: prior turns (capped) then the current message
	// with article context. Article body rides on the current message
	// only — past turns stay pure conversation, no stale body snapshots.
	const maxHistoryTurns = 20
	hist := req.History
	if len(hist) > maxHistoryTurns {
		hist = hist[len(hist)-maxHistoryTurns:]
	}
	messages := make([]Message, 0, len(hist)+1)
	for _, h := range hist {
		role := h.Role
		if role != "user" && role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		messages = append(messages, Message{Role: role, Content: h.Content})
	}
	messages = append(messages, Message{
		Role:    "user",
		Content: fmt.Sprintf(`Today is %s.\n\n%s%s`, today, req.Message, article_context),
	})
	// Detach from r.Context() so that an upstream proxy or browser-fetch
	// abort can't kill the LLM call mid-flight. Articles + history can
	// produce 50KB+ requests where prompt processing alone takes ~30s
	// before the first token; previously a 2-minute proxy cap was killing
	// these. The 10-minute cap below is the hard ceiling for a single
	// chat turn; the LLM client's own RequestTimeout still bounds header
	// wait independently.
	chatCtx, chatCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer chatCancel()

	// Heartbeat: keep the response connection alive while the LLM works
	// so reverse proxies (with default 60s/120s response-write caps) don't
	// drop the request. Writes a single whitespace byte every 25s to the
	// already-committed JSON response — JSON parsers tolerate leading
	// whitespace so the client's r.json() still parses correctly.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	var hbMu sync.Mutex
	headersCommitted := false
	flusher, _ := w.(http.Flusher)
	go func() {
		// Don't start heartbeat immediately — most errors return fast and
		// should use http.Error with a non-200 status. Wait long enough
		// to be confident the call is genuinely in-flight.
		select {
		case <-hbCtx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		// First heartbeat commits headers as 200 OK + JSON.
		hbMu.Lock()
		if !headersCommitted {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			headersCommitted = true
		}
		_, _ = w.Write([]byte(" "))
		if flusher != nil {
			flusher.Flush()
		}
		hbMu.Unlock()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				hbMu.Lock()
				_, _ = w.Write([]byte(" "))
				if flusher != nil {
					flusher.Flush()
				}
				hbMu.Unlock()
			}
		}
	}()

	// Privacy posture: techwriter chat ALWAYS uses the worker LLM
	// regardless of routing config. The article body is sensitive
	// (drafts, internal docs, customer data) and shouldn't leak to a
	// remote provider just because the operator's lead-routing fell
	// through. Suggest-title already uses WORKER explicitly; this
	// brings chat in line. Image generation is the only externally-
	// reachable techwriter feature, opted into per click.
	resp, err := T.AppCore.WorkerChat(chatCtx, messages,
		WithSystemPrompt(system_prompt),
		WithRouteKey("app.techwriter"))
	hbCancel()

	hbMu.Lock()
	defer hbMu.Unlock()
	if err != nil {
		// If the heartbeat already committed headers, we can't switch
		// to 500 — encode the error in the JSON body and let the client
		// surface it via the data.error path.
		if headersCommitted {
			json.NewEncoder(w).Encode(map[string]string{"error": "LLM error: " + err.Error()})
			return
		}
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))
	if !headersCommitted {
		w.Header().Set("Content-Type", "application/json")
		headersCommitted = true
	}

	// Parse ARTICLE: prefix. In chat mode, force type=chat even if the
	// LLM ignored the system-prompt rule and emitted an ARTICLE: body —
	// the whole point of the mode is that the editor stays untouched.
	if !chatOnly && strings.HasPrefix(text, "ARTICLE:") {
		article := strings.TrimSpace(strings.TrimPrefix(text, "ARTICLE:"))
		article = StripCodeFence(article)
		article = StripSourcesSection(article)
		if preserved_sources != "" {
			article += "\n" + preserved_sources
		}
		json.NewEncoder(w).Encode(map[string]string{"type": "article", "content": article})
	} else {
		// If chatOnly and the LLM ignored the rule and prefixed ARTICLE:
		// anyway, strip that prefix from the conversational text so the
		// reader doesn't see the sentinel label.
		if chatOnly {
			text = strings.TrimPrefix(text, "ARTICLE:")
			text = strings.TrimSpace(text)
		}
		json.NewEncoder(w).Encode(map[string]string{"type": "chat", "content": text})
	}
}

func (T *TechWriterAgent) handleSave(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		ImageURL string `json:"image_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	now := time.Now().Format(time.RFC3339)
	udb.Set(HistoryTable, req.ID, ArticleRecord{
		ID:       req.ID,
		Subject:  req.Subject,
		Body:     req.Body,
		Date:     now,
		ImageURL: req.ImageURL,
	})
	revID := UUIDv4()
	udb.Set(revisionTable, revID, ArticleRevision{
		ID:        revID,
		ArticleID: req.ID,
		Subject:   req.Subject,
		Body:      req.Body,
		Date:      now,
	})
	// Prune oldest revisions for this article, keeping the most recent maxArticleRevisions.
	type keyDate struct{ key, date string }
	var all []keyDate
	for _, k := range udb.Keys(revisionTable) {
		var rev ArticleRevision
		if udb.Get(revisionTable, k, &rev) && rev.ArticleID == req.ID {
			all = append(all, keyDate{k, rev.Date})
		}
	}
	if len(all) > maxArticleRevisions {
		sort.Slice(all, func(i, j int) bool { return all[i].date < all[j].date })
		for _, kd := range all[:len(all)-maxArticleRevisions] {
			udb.Unset(revisionTable, kd.key)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "rev_id": revID})
}

func (T *TechWriterAgent) handleList(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	type summary struct {
		ID      string `json:"ID"`
		Subject string `json:"Subject"`
		Date    string `json:"Date"`
	}
	var items []summary
	for _, key := range udb.Keys(HistoryTable) {
		var rec ArticleRecord
		if udb.Get(HistoryTable, key, &rec) {
			items = append(items, summary{ID: rec.ID, Subject: rec.Subject, Date: rec.Date})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (T *TechWriterAgent) handleLoad(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/load/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var rec ArticleRecord
	if !udb.Get(HistoryTable, id, &rec) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (T *TechWriterAgent) handleDelete(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/delete/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	udb.Unset(HistoryTable, id)
	w.WriteHeader(http.StatusOK)
}

func (T *TechWriterAgent) handleExport(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/export/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var rec ArticleRecord
	if !udb.Get(HistoryTable, id, &rec) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, sanitizeFilename(rec.Subject)))
	fmt.Fprint(w, exportHTML(rec))
}

func (T *TechWriterAgent) handleImport(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	buf := make([]byte, 512*1024) // 512KB max
	n, _ := file.Read(buf)
	html := string(buf[:n])

	// Extract title from <title> or <h1>.
	subject := ""
	if m := regexp.MustCompile(`<title>([^<]+)</title>`).FindStringSubmatch(html); m != nil {
		subject = strings.TrimSpace(m[1])
	} else if m := regexp.MustCompile(`<h1>([^<]+)</h1>`).FindStringSubmatch(html); m != nil {
		subject = strings.TrimSpace(m[1])
	}

	// Extract body content between <body> tags, strip HTML to approximate markdown.
	body := html
	if idx := strings.Index(body, "<body"); idx >= 0 {
		if end := strings.Index(body[idx:], ">"); end >= 0 {
			body = body[idx+end+1:]
		}
	}
	if idx := strings.Index(body, "</body>"); idx >= 0 {
		body = body[:idx]
	}

	// Strip the title/h1 and meta div (already captured as subject).
	body = regexp.MustCompile(`<h1>[^<]*</h1>`).ReplaceAllString(body, "")
	body = regexp.MustCompile(`<div class="meta">[\s\S]*?</div>`).ReplaceAllString(body, "")

	// Convert HTML back to markdown.
	body = HTMLToMarkdown(body)

	_ = header // unused but required by FormFile

	id := UUIDv4()
	udb.Set(HistoryTable, id, ArticleRecord{
		ID:      id,
		Subject: subject,
		Body:    body,
		Date:    time.Now().Format(time.RFC3339),
	})

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"%s","subject":%s}`, id, func() string {
		b, _ := json.Marshal(subject)
		return string(b)
	}())
}

func (T *TechWriterAgent) handleRevisions(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	articleID := strings.TrimPrefix(r.URL.Path, "/api/revisions/")
	if articleID == "" || udb == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	type RevisionSummary struct {
		ID   string `json:"id"`
		Date string `json:"date"`
	}
	var summaries []RevisionSummary
	for _, k := range udb.Keys(revisionTable) {
		var rev ArticleRevision
		if udb.Get(revisionTable, k, &rev) && rev.ArticleID == articleID {
			summaries = append(summaries, RevisionSummary{ID: rev.ID, Date: rev.Date})
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Date < summaries[j].Date })
	if summaries == nil {
		summaries = []RevisionSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summaries)
}

func (T *TechWriterAgent) handleRevision(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	revID := strings.TrimPrefix(r.URL.Path, "/api/revision/")
	if revID == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var rev ArticleRevision
	if !udb.Get(revisionTable, revID, &rev) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rev)
}

// handleGenerateImage generates an image using the blog image profile and
// returns either a remote URL (DALL-E) or a base64 data URL (Gemini).
// The frontend displays it as a preview and stores the URL for publishing.
func (T *TechWriterAgent) handleGenerateImage(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}

	if !ImageProfileAvailable("blog") && !ImageGenerationAvailable() {
		http.Error(w, "image generation not configured", http.StatusServiceUnavailable)
		return
	}

	result, err := GenerateImageLandscape(r.Context(), "", req.Prompt)
	if err != nil {
		http.Error(w, fmt.Sprintf("image generation failed: %s", err), http.StatusInternalServerError)
		return
	}

	// Resolve the result to a URL the browser can use.
	// DALL-E returns a remote HTTPS URL directly; Gemini saves to a local file.
	var imageURL string
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		imageURL = result.URL
	} else {
		// Read local file and return as a data URL so no static server is needed.
		data, err := os.ReadFile(result.URL)
		os.Remove(result.URL)
		if err != nil {
			http.Error(w, "failed to read generated image", http.StatusInternalServerError)
			return
		}
		mime := "image/png"
		if strings.HasSuffix(result.URL, ".jpg") || strings.HasSuffix(result.URL, ".jpeg") {
			mime = "image/jpeg"
		}
		imageURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": imageURL})
}

func (T *TechWriterAgent) handleSuggestTitle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		http.Error(w, "body required", http.StatusBadRequest)
		return
	}
	preview := req.Body
	if len(preview) > 2000 {
		preview = preview[:2000]
	}
	agent := &AppCore{LLM: T.AppCore.LLM}
	session := agent.CreateSession(WORKER)
	resp, err := session.Chat(r.Context(), []Message{
		{Role: "user", Content: "Suggest a concise title (max 8 words) for this article:\n\n" + preview},
	}, WithSystemPrompt("Reply with ONLY the title. No quotes. No trailing punctuation."), WithMaxTokens(32), WithThink(false))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	title := strings.TrimSpace(ResponseText(resp))
	title = strings.Trim(title, `"'`)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"subject": title})
}

func (T *TechWriterAgent) handleMergeSources(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if udb == nil {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
			return
		}
		var items []MergeSourceRecord
		for _, key := range udb.Keys(mergeSourceTable) {
			var rec MergeSourceRecord
			if udb.Get(mergeSourceTable, key, &rec) {
				items = append(items, rec)
			}
		}
		if items == nil {
			items = []MergeSourceRecord{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req MergeSourceRecord
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if req.ID == "" {
			req.ID = UUIDv4()
		}
		req.Date = time.Now().Format(time.RFC3339)
		udb.Set(mergeSourceTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *TechWriterAgent) handleMergeSource(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/merge-source/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec MergeSourceRecord
		if !udb.Get(mergeSourceTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		udb.Unset(mergeSourceTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMerge combines the current editor content with pasted text into one
// cohesive article, preserving Sources sections and respecting the current mode.
func (T *TechWriterAgent) handleMerge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		Other    string `json:"other"`
		Mode     string `json:"mode"`
		Guidance string `json:"guidance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Body) == "" || strings.TrimSpace(req.Other) == "" {
		http.Error(w, "both body and other content required", http.StatusBadRequest)
		return
	}

	// Strip and preserve Sources from both.
	body_a, sources_a := SplitSourcesSection(req.Body)
	body_b, sources_b := SplitSourcesSection(req.Other)

	// Merge the two Sources sections (dedup by URL).
	merged_sources := mergeSourceSections(sources_a, sources_b)

	system_prompt := T.activeDefaultPrompt()

	guidance := req.Guidance
	if guidance == "" {
		guidance = "Merge these into one cohesive article. Preserve all key facts from both. Eliminate duplication. Organize around the strongest narrative."
	}

	today := time.Now().Format("January 2, 2006")

	merge_prompt := fmt.Sprintf(`Today is %s.

Merge these two pieces of content into ONE cohesive article.

%s

=== CONTENT 1 ===
%s

=== CONTENT 2 ===
%s

%s

The merged article must preserve all important facts, data, and claims from both sources. Write it so the reader has no idea it came from two separate pieces.`,
		today, guidance, body_a, body_b,
		func() string {
			if merged_sources != "" && req.Mode == "blog" {
				return "\nSOURCE REFERENCE (use these to name sources naturally — do NOT reproduce this section):\n" + merged_sources
			}
			return ""
		}())

	// Same privacy posture as handleChat — never escalate to lead.
	// Article bodies stay on the local worker LLM.
	resp, err := T.AppCore.WorkerChat(r.Context(), []Message{
		{Role: "user", Content: merge_prompt},
	}, WithSystemPrompt(system_prompt),
		WithRouteKey("app.techwriter"))

	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))

	// Parse TITLES:/TITLE: and IMAGES:/IMAGE: prefix.
	title := ""
	merge_image_prompt := ""
	if strings.HasPrefix(text, "TITLES:") {
		lines := strings.SplitN(text, "\n", 2)
		raw := strings.TrimSpace(strings.TrimPrefix(lines[0], "TITLES:"))
		if parts := strings.SplitN(raw, "|", 2); len(parts) > 0 {
			title = strings.TrimSpace(parts[0])
		}
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		}
	} else if strings.HasPrefix(text, "TITLE:") {
		lines := strings.SplitN(text, "\n", 2)
		title = strings.TrimSpace(strings.TrimPrefix(lines[0], "TITLE:"))
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		}
	}
	if strings.HasPrefix(text, "IMAGES:") {
		lines := strings.SplitN(text, "\n", 2)
		raw := strings.TrimSpace(strings.TrimPrefix(lines[0], "IMAGES:"))
		if parts := strings.SplitN(raw, "|", 2); len(parts) > 0 {
			merge_image_prompt = strings.TrimSpace(parts[0])
		}
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		}
	} else if strings.HasPrefix(text, "IMAGE:") {
		lines := strings.SplitN(text, "\n", 2)
		merge_image_prompt = strings.TrimSpace(strings.TrimPrefix(lines[0], "IMAGE:"))
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		}
	}

	article := text
	if strings.HasPrefix(article, "ARTICLE:") {
		article = strings.TrimSpace(strings.TrimPrefix(article, "ARTICLE:"))
	}
	article = StripCodeFence(article)

	// Strip any Sources the LLM generated, reattach the merged sources.
	article = StripSourcesSection(article)
	if merged_sources != "" {
		article += "\n" + merged_sources
	}

	result := map[string]string{"content": article}
	if title != "" {
		result["title"] = title
	}
	if merge_image_prompt != "" {
		result["image_prompt"] = merge_image_prompt
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// mergeSourceSections combines two ## Sources sections, deduplicating by URL.
func mergeSourceSections(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}

	url_re := regexp.MustCompile(`https?://[^\s]+`)
	seen := make(map[string]bool)
	var merged strings.Builder
	merged.WriteString("\n## Sources\n")

	process := func(section string) {
		for _, line := range strings.Split(section, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "## Sources") {
				continue
			}
			urls := url_re.FindAllString(line, -1)
			if len(urls) == 0 {
				continue
			}
			url := strings.TrimRight(urls[0], ".,;:)")
			if seen[url] {
				continue
			}
			seen[url] = true
			merged.WriteString(line)
			merged.WriteString("\n")
		}
	}
	process(a)
	process(b)
	return merged.String()
}

// HTMLToMarkdown, HTMLUnescape, HTMLEscape, MarkdownToHTML from core/text.go

func (T *TechWriterAgent) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		ImageURL string `json:"image_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		http.Error(w, "body required", http.StatusBadRequest)
		return
	}
	html := headerImageHTML(req.ImageURL) + MarkdownToHTML(req.Body)
	// Build a separate "full" markdown for the Copy Markdown button.
	// The raw req.Body alone is missing things the rendered HTML has:
	//   - the header image (lives on the article record, not in body)
	//   - the auto-generated Table of Contents (server-injected)
	//   - bare-domain URLs need promotion to https:// or Confluence
	//     won't recognize them as links
	exportableMD := buildExportableMarkdown(req.Subject, req.Body, req.ImageURL)
	title := req.Subject
	if title == "" {
		title = "Preview"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; max-width: 800px; margin: 2rem auto; padding: 0 1rem; color: #172b4d; line-height: 1.6; }
h1 { font-size: 1.7rem; border-bottom: 1px solid #dfe1e6; padding-bottom: 0.5rem; }
h2 { font-size: 1.4rem; margin-top: 1.5rem; }
h3 { font-size: 1.15rem; margin-top: 1.2rem; }
pre { background: #f4f5f7; border: 1px solid #dfe1e6; border-radius: 3px; padding: 0.8rem; overflow-x: auto; font-size: 0.85rem; }
code { background: #f4f5f7; padding: 0.15rem 0.3rem; border-radius: 3px; font-size: 0.9em; }
pre code { background: none; padding: 0; }
table { border-collapse: collapse; width: 100%%; margin: 1rem 0; }
table { margin: 1rem 0; }              /* left-aligned by default; override the centering some hosts apply */
th, td { border: 1px solid #dfe1e6; padding: 0.5rem 0.8rem; text-align: left !important; }
th { background: #f4f5f7; font-weight: 600; text-align: left !important; }
blockquote { border-left: 3px solid #0052cc; background: #deebff; padding: 0.8rem 1rem; margin: 1rem 0; border-radius: 3px; }
ul, ol { padding-left: 1.5rem; }
.header-image { width: 100%%; max-height: 320px; object-fit: cover; border-radius: 8px; margin-bottom: 1.5rem; }
.auto-toc { background: #f4f5f7; border: 1px solid #dfe1e6; border-radius: 4px; padding: 0.7rem 0.9rem; margin: 1rem 0 1.5rem; }
.auto-toc-h { font-size: 0.78rem; color: #5e6c84; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.4rem; }
.auto-toc ol { margin: 0; padding-left: 1.4rem; }
.auto-toc li { margin: 0.2rem 0; }
.auto-toc a { color: #0052cc; text-decoration: none; }
.auto-toc a:hover { text-decoration: underline; }
li { margin: 0.3rem 0; }
.copy-hint { position: fixed; top: 0; left: 0; right: 0; background: #0052cc; color: white; text-align: center; padding: 0.5rem; font-size: 0.85rem; z-index: 100; }
/* Anchor jumps land behind the fixed copy-hint bar without this.
 * 4rem covers the bar plus a little visual breathing room so the
 * scrolled-to heading sits comfortably below the top edge. */
h1, h2, h3 { scroll-margin-top: 4rem; }
html { scroll-padding-top: 4rem; }
</style></head><body>
<div class="copy-hint">
  <button onclick="copyContent()" style="background:white;color:#0052cc;border:none;border-radius:4px;padding:0.3rem 1rem;cursor:pointer;font-weight:600;margin-right:0.5rem">Copy HTML</button>
  <button onclick="copyMarkdown()" style="background:white;color:#0052cc;border:none;border-radius:4px;padding:0.3rem 1rem;cursor:pointer;font-weight:600;margin-right:0.5rem">Copy Markdown</button>
  HTML preserves formatting; Markdown survives Confluence's paste sanitizer
</div>
<div id="content">
<h1>%s</h1>
%s
</div>
<script>
function copyContent() {
  var content = document.getElementById('content');
  var html = content.innerHTML;
  var text = content.innerText;
  var btn = document.querySelector('.copy-hint button');
  // Modern path: write text/html + text/plain explicitly via the
  // Clipboard API. Selection-based execCommand('copy') drops images
  // and external <a> tags in many paste targets (Confluence in
  // particular — it sees the selection as a "structured fragment"
  // and runs an aggressive sanitizer). Writing text/html as a
  // first-class clipboard format preserves <img> and <a> intact.
  if (navigator.clipboard && window.ClipboardItem) {
    var item = new ClipboardItem({
      'text/html':  new Blob([html], {type: 'text/html'}),
      'text/plain': new Blob([text], {type: 'text/plain'}),
    });
    navigator.clipboard.write([item]).then(function() {
      btn.textContent = 'Copied!';
      setTimeout(function() { btn.textContent = 'Copy to Clipboard'; }, 2000);
    }).catch(function() {
      // Permissions denied or older browser — fall back to selection copy.
      legacyCopy(content, btn);
    });
    return;
  }
  legacyCopy(content, btn);
}
function legacyCopy(content, btn) {
  var range = document.createRange();
  range.selectNodeContents(content);
  var sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  try { document.execCommand('copy'); } catch (e) {}
  sel.removeAllRanges();
  btn.textContent = 'Copied (legacy)';
  setTimeout(function() { btn.textContent = 'Copy to Clipboard'; }, 2000);
}
// Copy the original markdown source. Pastes cleanly into Confluence
// (and other markdown-aware editors) without the rich-text sanitizer
// stripping images and external links.
function copyMarkdown() {
  var md = document.getElementById('md-source').textContent;
  var btn = event.target;
  var orig = btn.textContent;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(md).then(function() {
      btn.textContent = 'Copied!';
      setTimeout(function() { btn.textContent = orig; }, 2000);
    }).catch(function() { btn.textContent = 'Copy failed'; });
  }
}
</script>
<pre id="md-source" style="display:none">%s</pre>
</body></html>`, HTMLEscape(title), HTMLEscape(title), html, HTMLEscape(exportableMD))
}

// stripMarkdownTOC removes a "## Table of Contents" / "## Contents" /
// "## TOC" block and everything under it up to the next heading. The
// match is case-insensitive and tolerates trailing punctuation. Used
// by the markdown-export path so the LLM-written TOC doesn't paste
// into Confluence Cloud as broken anchor links — Confluence's `{toc}`
// macro handles TOCs natively post-paste.
func stripMarkdownTOC(md string) string {
	var out []string
	inTOC := false
	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		isHeading := strings.HasPrefix(trimmed, "# ") ||
			strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ")
		if isHeading {
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			title = strings.TrimRight(title, ":.")
			tl := strings.ToLower(title)
			if tl == "table of contents" || tl == "contents" || tl == "toc" {
				inTOC = true
				continue
			}
			inTOC = false
		}
		if inTOC {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// buildExportableMarkdown assembles the markdown blob that the Copy
// Markdown button hands to the clipboard. It augments the raw editor
// body with the header image (markdown image reference at the top)
// and bare-domain URL promotion (so Confluence's pasted-link
// auto-detector recognizes them as URLs). The LLM-written TOC is
// stripped on this path because Confluence Cloud kills heading IDs;
// users add `{toc}` post-paste for a working contents list.
func buildExportableMarkdown(subject, body, imageURL string) string {
	var b strings.Builder

	if subject != "" {
		fmt.Fprintf(&b, "# %s\n\n", subject)
	}
	if imageURL = strings.TrimSpace(imageURL); imageURL != "" {
		alt := subject
		if alt == "" {
			alt = "header image"
		}
		fmt.Fprintf(&b, "![%s](%s)\n\n", alt, imageURL)
	}

	// Strip the LLM's title heading (if any) — we already wrote it
	// as the level-1 heading above, so duplicating it would just
	// produce two H1s in the pasted output.
	body = strings.TrimSpace(body)
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) > 0 && strings.HasPrefix(strings.TrimSpace(bodyLines[0]), "# ") {
		bodyLines = bodyLines[1:]
		body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	}

	// Strip the LLM-written "## Table of Contents" block. Confluence
	// Cloud strips heading IDs on paste, so the TOC's [text](#anchor)
	// links would all paste as broken non-resolving links. The user's
	// next move post-paste is to insert Confluence's `{toc}` macro
	// (one keystroke: /Table of Contents) which auto-builds a working
	// TOC from the page's headings. The HTML copy path keeps the TOC
	// intact for non-Confluence destinations (preview, GitHub, Notion).
	body = stripMarkdownTOC(body)

	// Normalize bare-domain markdown links: `[text](github.com/foo)` →
	// `[text](https://github.com/foo)`. Confluence pastes the literal
	// `(github.com/foo)` URL otherwise, which it can't render as a
	// link target. The pattern matches text, captures the URL, and
	// promotes when the URL has no scheme but contains a dot.
	bareDomainLink := regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s#/][^)\s]*\.[^)\s]*)\)`)
	body = bareDomainLink.ReplaceAllStringFunc(body, func(m string) string {
		sub := bareDomainLink.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		text, url := sub[1], sub[2]
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") ||
			strings.HasPrefix(url, "mailto:") || strings.HasPrefix(url, "tel:") {
			return m
		}
		return "[" + text + "](https://" + url + ")"
	})
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '"' || r == ':' || r == '*' || r == '?' || r == '<' || r == '>' || r == '|' {
			return '-'
		}
		return r
	}, s)
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.TrimSpace(s)
}

func exportHTML(rec ArticleRecord) string {
	date := rec.Date
	if t, err := time.Parse(time.RFC3339, rec.Date); err == nil {
		date = t.Format("January 2, 2006")
	}
	words := len(strings.Fields(rec.Body))
	read_time := max(1, words/250)

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<style>
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    max-width: 800px; margin: 0 auto; padding: 2rem 1rem;
    color: #24292f; line-height: 1.6;
  }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .meta { color: #656d76; font-size: 0.85rem; margin-bottom: 2rem; padding-bottom: 1rem; border-bottom: 1px solid #d0d7de; }
  .meta span { margin-right: 1.5rem; }
  h2 { font-size: 1.3rem; margin-top: 2rem; padding-bottom: 0.3rem; border-bottom: 1px solid #d0d7de; }
  h3 { font-size: 1.1rem; margin-top: 1.5rem; }
  pre { background: #f6f8fa; border: 1px solid #d0d7de; border-radius: 6px; padding: 1rem; overflow-x: auto; font-size: 0.85rem; }
  code { background: #f6f8fa; padding: 0.15rem 0.3rem; border-radius: 3px; font-size: 0.9em; }
  pre code { background: none; padding: 0; }
  ul, ol { margin: 0.5rem 0 0.5rem 1.5rem; }
  li { margin-bottom: 0.3rem; }
  blockquote { border-left: 3px solid #d0d7de; margin: 1rem 0; padding: 0.5rem 1rem; color: #656d76; }
  table { border-collapse: collapse; width: auto; margin: 1rem 0; }
  th, td { border: 1px solid #d0d7de; padding: 0.5rem; text-align: left !important; }
  th { background: #f6f8fa; text-align: left !important; }
  .note { background: #ddf4ff; border: 1px solid #54aeff; border-radius: 6px; padding: 0.75rem 1rem; margin: 1rem 0; }
  .warning { background: #fff8c5; border: 1px solid #d4a72c; border-radius: 6px; padding: 0.75rem 1rem; margin: 1rem 0; }
  .header-image { width: 100%%; max-height: 320px; object-fit: cover; border-radius: 8px; margin-bottom: 1.5rem; }
  /* Auto-generated Table of Contents (numbered list of section links). */
  .auto-toc { background: #f6f8fa; border: 1px solid #d0d7de; border-radius: 6px; padding: 0.8rem 1rem; margin: 1rem 0 2rem; }
  .auto-toc-h { font-size: 0.78rem; color: #656d76; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.4rem; }
  .auto-toc ol { margin: 0; padding-left: 1.4rem; }
  .auto-toc li { margin: 0.2rem 0; }
  .auto-toc a { color: #0969da; text-decoration: none; }
  .auto-toc a:hover { text-decoration: underline; }
  /* Anchor jumps get a little headroom so the scrolled-to heading
   * doesn't sit flush against the top edge of the viewport. */
  h1, h2, h3 { scroll-margin-top: 1.5rem; }
  html { scroll-padding-top: 1.5rem; }
</style>
</head>
<body>
%s<h1>%s</h1>
<div class="meta"><span>%s</span><span>%d words</span><span>%d min read</span></div>
%s
</body>
</html>`,
		HTMLEscape(rec.Subject),
		headerImageHTML(rec.ImageURL),
		HTMLEscape(rec.Subject),
		date, words, read_time,
		MarkdownToHTML(rec.Body))
}

// headerImageHTML returns an <img> tag for the article's header image,
// or empty string when no image is set. Used by both the export and
// preview renderers so the published artifact and the browser preview
// always agree.
func headerImageHTML(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	return fmt.Sprintf(`<img class="header-image" src="%s" alt="">`, HTMLEscape(url))
}

// Use shared functions from core:
// HTMLEscape, MarkdownToHTML, InlineMarkdownToHTML, HTMLToMarkdown, HTMLUnescape

// techwriterCSS holds techwriter-specific styles. The shared dark theme,
// buttons, scrollbars, and modal primitives come from webui.BaseCSS().
const techwriterCSS = `
  body { height: 100vh; display: flex; flex-direction: column; }
  .rev-nav { display: flex; align-items: center; gap: 0.25rem; flex-shrink: 0; }
  .rev-nav button {
    background: transparent; border: 1px solid #30363d; color: #8b949e;
    border-radius: 4px; padding: 0.2rem 0.45rem; cursor: pointer; font-size: 0.75rem; line-height: 1;
  }
  .rev-nav button:disabled { opacity: 0.3; cursor: default; }
  .rev-nav button:not(:disabled):hover { color: #c9d1d9; border-color: #8b949e; }
  #rev-indicator { font-size: 0.72rem; color: #8b949e; white-space: nowrap; min-width: 3.5rem; text-align: center; }
  #rev-mark-latest { background: #1f3a2a !important; border-color: #238636 !important; color: #3fb950 !important; }
  #rev-mark-latest:hover { background: #238636 !important; color: #fff !important; }
  #toolbar {
    display: flex; align-items: center; gap: 0.5rem; padding: 0.5rem 1rem;
    background: #161b22; border-bottom: 1px solid #21262d;
  }
  #toolbar input {
    flex: 1; background: #0d1117; border: 1px solid #30363d; color: #c9d1d9;
    padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.9rem;
  }
  #toolbar button {
    background: #238636; color: #fff; border: none; border-radius: 6px;
    padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem; white-space: nowrap;
  }
  #toolbar button.secondary {
    background: transparent; border: 1px solid #30363d; color: #8b949e;
  }
  #toolbar button:hover { opacity: 0.9; }
  #main {
    display: flex; flex: 1; overflow: hidden;
  }
  #editor-pane {
    flex: 1; display: flex; flex-direction: column; border-right: 1px solid #21262d;
    min-width: 0; /* allow flex shrinkage when diff content is wide */
  }
  #editor {
    flex: 1; background: #0d1117; color: #c9d1d9; border: none; resize: none;
    padding: 1rem; font-family: 'SF Mono', 'Fira Code', monospace; font-size: 0.85rem;
    line-height: 1.6; outline: none;
  }
  #merge-toggle {
    display: flex; align-items: center; gap: 0.4rem; padding: 0.3rem 0.75rem;
    background: #161b22; border-top: 1px solid #21262d; cursor: pointer;
    font-size: 0.8rem; color: #8b949e; user-select: none; flex-shrink: 0;
  }
  #merge-toggle:hover { color: #c9d1d9; }
  #merge-toggle .arr { font-size: 0.6rem; transition: transform 0.15s; }
  #merge-toggle .arr.open { transform: rotate(90deg); }
  .merge-actions { margin-left: auto; display: flex; align-items: center; gap: 0.3rem; flex-shrink: 0; }
  .merge-btn {
    background: transparent; border: 1px solid #30363d; color: #8b949e;
    border-radius: 3px; padding: 0.1rem 0.5rem; cursor: pointer; font-size: 0.7rem;
  }
  .merge-btn:hover { color: #c9d1d9; border-color: #8b949e; }
  .merge-run-btn {
    background: #238636; border: none; color: #fff; border-radius: 4px;
    padding: 0.2rem 0.6rem; cursor: pointer; font-size: 0.75rem;
  }
  .merge-run-btn:hover { opacity: 0.9; }
  #merge-guidance-inline {
    background: #0d1117; border: 1px solid #30363d; color: #c9d1d9;
    padding: 0.2rem 0.5rem; border-radius: 4px; font-size: 0.75rem; width: 180px;
  }
  #merge-guidance-inline:focus { border-color: #58a6ff; outline: none; }
  #merge-source-current { color: #58a6ff; font-size: 0.75rem; }
  #merge-source-pane { display: none; border-top: 1px solid #21262d; flex-direction: column; }
  #merge-source-pane.open { display: flex; height: 200px; min-height: 80px; max-height: 85%; }
  #merge-source-resizer { height: 5px; background: #21262d; cursor: row-resize; flex-shrink: 0; }
  #merge-source-resizer:hover, #merge-source-resizer.dragging { background: #58a6ff; }
  #merge-source-pane textarea {
    flex: 1; background: #0d1117; color: #c9d1d9; border: none; resize: none;
    padding: 0.75rem 1rem; font-family: inherit; font-size: 0.85rem; line-height: 1.6; outline: none;
  }
  #merge-sources-panel {
    display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 1rem; width: 500px; max-height: 70vh; overflow-y: auto; z-index: 100;
  }
  #merge-sources-panel h3 { color: #c9d1d9; margin-bottom: 0.75rem; }
  .merge-source-item {
    display: flex; justify-content: space-between; align-items: center;
    padding: 0.5rem 0.75rem; border: 1px solid #21262d; border-radius: 6px;
    margin-bottom: 0.4rem; cursor: pointer; background: #0d1117;
  }
  .merge-source-item:hover { border-color: #30363d; background: #1c2128; }
  .merge-source-item .title { color: #c9d1d9; font-weight: 600; font-size: 0.85rem; }
  .merge-source-item .date { color: #484f58; font-size: 0.75rem; }
  .merge-source-item .del-btn { background: none; border: none; color: #484f58; cursor: pointer; font-size: 0.85rem; }
  .merge-source-item .del-btn:hover { color: #f85149; }
  #chat-resizer {
    width: 5px; background: var(--border); cursor: col-resize; flex-shrink: 0;
  }
  #chat-resizer:hover, #chat-resizer.dragging {
    background: var(--accent);
  }
  #chat-pane {
    width: 380px; display: flex; flex-direction: column; background: #161b22;
  }
  #chat-header {
    padding: 0.5rem 1rem; border-bottom: 1px solid #21262d; font-size: 0.85rem;
    color: #8b949e; font-weight: 600;
  }
  #chat-messages {
    flex: 1; overflow-y: auto; padding: 0.75rem;
  }
  .chat-msg {
    margin-bottom: 0.75rem; padding: 0.6rem 0.8rem; border-radius: 8px;
    font-size: 0.85rem; line-height: 1.5;
  }
  .chat-msg.user {
    background: #1f2937; color: #c9d1d9; margin-left: 2rem;
  }
  .chat-msg.assistant {
    background: #0d1117; color: #c9d1d9; border: 1px solid #21262d;
  }
  .chat-msg.assistant .apply-btn {
    display: inline-block; margin-top: 0.4rem; background: #238636; color: #fff;
    border: none; border-radius: 4px; padding: 0.2rem 0.5rem; cursor: pointer;
    font-size: 0.75rem;
  }
  .chat-msg pre { background: #161b22; border: 1px solid #30363d; border-radius: 4px; padding: 0.5rem; margin: 0.4rem 0; overflow-x: auto; font-size: 0.8rem; }
  .chat-msg code { background: #161b22; padding: 0.1rem 0.25rem; border-radius: 3px; font-size: 0.85em; }
  .chat-msg pre code { background: none; padding: 0; }
  #chat-input-area {
    display: flex; gap: 0.5rem; padding: 0.5rem 0.75rem 1.25rem;
    border-top: 1px solid #21262d;
    align-items: flex-end;
  }
  #chat-input {
    flex: 1; background: #0d1117; border: 1px solid #30363d; color: #c9d1d9;
    padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
    font-family: inherit; resize: vertical; min-height: 80px; max-height: 300px;
  }
  #chat-input:focus { border-color: #58a6ff; outline: none; }
  #chat-send, #chat-talk {
    border: none; border-radius: 6px;
    padding: 0.4rem 0.8rem; cursor: pointer; font-size: 0.8rem;
  }
  #chat-send {
    background: #238636; color: #fff;
  }
  #chat-talk {
    background: #0d1117; color: #c9d1d9; border: 1px solid #30363d;
  }
  #chat-talk:hover { border-color: #58a6ff; color: #fff; }
  .mode-tag {
    display: inline-block; font-size: 0.65rem; text-transform: uppercase;
    letter-spacing: 0.05em; padding: 0.05rem 0.35rem; border-radius: 3px;
    background: rgba(255,255,255,0.15); color: #fff; margin-right: 0.4rem;
    vertical-align: middle;
  }
  #articles-panel {
    display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 1rem; width: 500px; max-height: 70vh; overflow-y: auto; z-index: 100;
  }
  .art-panel-hdr { display:flex; align-items:center; justify-content:space-between; margin-bottom:0.75rem; }
  .art-panel-hdr h3 { color: #c9d1d9; margin: 0; }
  .art-sort-bar { display:flex; gap:2px; }
  .art-sort-btn { background:#0d1117; border:1px solid #30363d; color:#8b949e; font-size:0.72rem; padding:2px 8px; border-radius:4px; cursor:pointer; }
  .art-sort-btn.active { background:#388bfd; border-color:#388bfd; color:#fff; }
  .article-item {
    display: flex; justify-content: space-between; align-items: center;
    padding: 0.5rem 0.75rem; border: 1px solid #21262d; border-radius: 6px;
    margin-bottom: 0.4rem; cursor: pointer; background: #0d1117;
  }
  .article-item:hover { border-color: #30363d; background: #1c2128; }
  .article-item .title { color: #c9d1d9; font-weight: 600; font-size: 0.85rem; }
  .article-item .date { color: #484f58; font-size: 0.75rem; }
  .article-item .del-btn {
    background: none; border: none; color: #484f58; cursor: pointer; font-size: 0.85rem;
  }
  .article-item .del-btn:hover { color: #f85149; }
  #overlay {
    display: none; position: fixed; top: 0; left: 0; width: 100%; height: 100%;
    background: rgba(0,0,0,0.5); z-index: 99;
  }
  .spinner { display: inline-block; width: 14px; height: 14px; border: 2px solid #30363d; border-top-color: #58a6ff; border-radius: 50%; animation: spin 0.8s linear infinite; vertical-align: middle; margin-right: 0.3rem; }
  @keyframes spin { to { transform: rotate(360deg); } }
  #diagram-modal {
    display: none; position: fixed; top: 50%; left: 50%; transform: translate(-50%,-50%);
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    width: min(940px, 92vw); height: min(620px, 88vh);
    flex-direction: column; z-index: 100;
  }
  #diagram-modal.open { display: flex; }
  #diagram-modal-hdr {
    display: flex; justify-content: space-between; align-items: center;
    padding: 0.6rem 1rem; border-bottom: 1px solid #21262d; flex-shrink: 0;
  }
  #diagram-modal-hdr span { font-size: 0.9rem; font-weight: 600; color: #c9d1d9; }
  #diagram-modal-hdr button {
    background: none; border: none; color: #8b949e; cursor: pointer;
    font-size: 1rem; padding: 0.2rem 0.4rem; border-radius: 4px;
  }
  #diagram-modal-hdr button:hover { color: #f85149; }
  #diagram-body { display: flex; flex: 1; overflow: hidden; }
  #diagram-left {
    width: 290px; flex-shrink: 0; display: flex; flex-direction: column;
    gap: 0.5rem; padding: 0.75rem; border-right: 1px solid #21262d;
  }
  #diagram-source {
    flex: 1; background: #0d1117; border: 1px solid #30363d; color: #c9d1d9;
    border-radius: 6px; padding: 0.5rem 0.6rem; font-family: 'SF Mono','Fira Code',monospace;
    font-size: 0.78rem; line-height: 1.5; resize: none; outline: none; min-height: 0;
  }
  #diagram-source:focus { border-color: #58a6ff; }
  #diagram-left-btns { display: flex; gap: 0.4rem; flex-shrink: 0; }
  #diagram-left-btns button {
    flex: 1; background: #21262d; border: 1px solid #30363d; color: #c9d1d9;
    border-radius: 6px; padding: 0.35rem 0.5rem; cursor: pointer; font-size: 0.78rem;
  }
  #diagram-left-btns button:hover { border-color: #58a6ff; color: #58a6ff; }
  #diagram-left-btns button.primary { background: #238636; border: none; color: #fff; }
  #diagram-left-btns button.primary:hover { opacity: 0.9; }
  #diagram-picker { display: flex; flex-direction: column; gap: 0.25rem; max-height: 110px; overflow-y: auto; flex-shrink: 0; }
  .diagram-pick-btn {
    background: #0d1117; border: 1px solid #21262d; color: #8b949e;
    border-radius: 4px; padding: 0.25rem 0.5rem; cursor: pointer; font-size: 0.72rem;
    text-align: left; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .diagram-pick-btn:hover { border-color: #58a6ff; color: #c9d1d9; }
  #diagram-right { flex: 1; display: flex; flex-direction: column; min-width: 0; }
  #diagram-preview-area {
    flex: 1; overflow: auto; padding: 1.25rem; display: flex;
    align-items: center; justify-content: center; background: #0d1117;
  }
  #diagram-preview svg { max-width: 100%; height: auto; }
  #diagram-preview .diagram-hint { color: #484f58; font-size: 0.85rem; }
  #diagram-export {
    display: flex; gap: 0.5rem; padding: 0.65rem 0.75rem;
    border-top: 1px solid #21262d; justify-content: flex-end; flex-shrink: 0;
  }
  #diagram-export button {
    background: #21262d; border: 1px solid #30363d; color: #c9d1d9;
    border-radius: 6px; padding: 0.35rem 0.8rem; cursor: pointer; font-size: 0.8rem;
  }
  #diagram-export button:hover { border-color: #58a6ff; color: #58a6ff; }
  #diagram-export button.primary { background: #1a7f37; border-color: #2ea043; color: #fff; }
  #diagram-export button.primary:hover { background: #238636; }
`

// techwriterBody is the app's main HTML panel, inserted into the
// shared scaffolding by webui.RenderPage.
const techwriterBody = `
<div id="toolbar">
  <span class="app-title">TechWriter</span>
  <button class="secondary" onclick="showArticles()">Articles</button>
  <span class="rev-nav">
    <button id="rev-back" onclick="navigateRevision(-1)" disabled title="Previous revision">&#9664;</button>
    <span id="rev-indicator"></span>
    <button id="rev-fwd" onclick="navigateRevision(1)" disabled title="Next revision">&#9654;</button>
    <button id="rev-mark-latest" class="secondary" onclick="markAsLatest()" style="display:none">Make Latest</button>
  </span>
  <input id="subject" type="text" placeholder="Article subject..." style="flex:1">
  <select id="persona-select" style="padding:0.4rem;background:#21262d;color:#c9d1d9;border:1px solid #30363d;border-radius:6px;font-size:0.85rem;max-width:150px" onchange="onPersonaChange()">
    <option value="">Default Style</option>
  </select>
  <button class="secondary" onclick="showNewPersonaMenu()" title="Add writing style">+ Persona</button>
  <input type="file" id="persona-file" accept=".pdf" style="display:none" onchange="uploadPersona(this)">
  <button onclick="saveArticle()">Save</button>
  <button id="fill-gaps-btn" onclick="fillGaps()">Process</button>
  <button class="secondary" onclick="exportArticle()">Export</button>
  <button class="secondary" onclick="previewForCopy()" title="Preview for copy-paste into Confluence">Preview</button>
  <button class="secondary" onclick="openDiagramTool()" title="Render and export Mermaid diagrams">Diagrams</button>
  <button class="secondary" onclick="document.getElementById('import-file').click()">Import</button>
  <input type="file" id="import-file" accept=".html,.htm" style="display:none" onchange="importArticle(this)">
  <button class="secondary" onclick="toggleMergeSource()">Merge</button>
  <button class="secondary" onclick="newArticle()">New</button>
  <button class="secondary" onclick="resetPage()" title="Reset everything" style="color:#f85149">Reset</button>
</div>
<div id="main">
  <div id="editor-pane">
    <textarea id="editor" placeholder="Write your instructions here...

Paste commands, bullet points, or partial steps — the LLM will fill in the details.

Examples:
  1. SSH to prod-web-01
  2. systemctl restart nginx
  3. Check the logs

Click 'Process' or use the chat to expand your content."></textarea>
    <div id="merge-toggle" onclick="toggleMergeSource()">
      <span class="arr" id="merge-arr">&#9654;</span> Merge Source
      <span class="merge-actions">
        <input id="merge-guidance-inline" type="text" placeholder="Merge guidance (optional)..." onclick="event.stopPropagation()">
        <button class="merge-btn" onclick="event.stopPropagation();saveMergeSource()">Save</button>
        <button class="merge-btn" onclick="event.stopPropagation();showMergeSources()">Load</button>
        <button class="merge-btn" onclick="event.stopPropagation();importMergeSource()" title="Import file">Import</button>
        <button id="merge-run" class="merge-run-btn" onclick="event.stopPropagation();runMerge()">Merge</button>
        <span id="merge-source-current"></span>
      </span>
    </div>
    <div id="merge-source-pane">
      <div id="merge-source-resizer" onmousedown="startMergeSourceResize(event)"></div>
      <textarea id="merge-source-input" placeholder="Paste the second article or content here..."></textarea>
    </div>
  </div>
  <div id="chat-resizer" onmousedown="startChatResize(event)"></div>
  <div id="chat-pane">
    <div id="chat-header">Chat <button onclick="document.getElementById('chat-messages').innerHTML='';clearChatHistory();" style="float:right;background:none;border:none;color:#8b949e;cursor:pointer;font-size:0.75rem;padding:0 0.3rem" title="Clear chat">Clear</button></div>
    <div id="chat-messages"></div>
    <div id="chat-input-area">
      <textarea id="chat-input" rows="3" placeholder="Discuss with Chat, or click Edit to apply changes. Enter = Edit, Alt+Enter = Chat, Shift+Enter = newline." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendChat(event.altKey?'chat':'edit');}"></textarea>
      <button id="chat-talk" onclick="sendChat('chat')" title="Discuss without changing the article">Chat</button>
      <button id="chat-send" onclick="sendChat('edit')" title="Propose a rewrite to apply to the article">Edit</button>
    </div>
  </div>
</div>
<div id="overlay" onclick="hideArticles()"></div>
<div id="diagram-modal">
  <div id="diagram-modal-hdr">
    <span>Diagram Tool</span>
    <button onclick="closeDiagramTool()" title="Close">&#10005;</button>
  </div>
  <div id="diagram-body">
    <div id="diagram-left">
      <textarea id="diagram-source" placeholder="Paste Mermaid source here, or click Scan Document to find diagrams in the current article..."></textarea>
      <div id="diagram-left-btns">
        <button onclick="scanForMermaid()">Scan Document</button>
        <button class="primary" onclick="renderDiagramPreview()">Render</button>
      </div>
      <div id="diagram-picker"></div>
    </div>
    <div id="diagram-right">
      <div id="diagram-preview-area">
        <div id="diagram-preview"><span class="diagram-hint">Paste or scan for a diagram, then click Render.</span></div>
      </div>
      <div id="diagram-export">
        <button class="primary" onclick="downloadDiagramDrawio()" title="Import into Gliffy via draw.io import">Download draw.io</button>
        <button onclick="downloadDiagramPNG()">Download PNG</button>
        <button onclick="downloadDiagramSVG()">Download SVG</button>
      </div>
    </div>
  </div>
</div>
<div id="articles-panel"><div class="art-panel-hdr"><h3>Saved Articles</h3><div class="art-sort-bar"><button id="art-sort-date" class="art-sort-btn active" onclick="setArticleSort('date')">Newest</button><button id="art-sort-name" class="art-sort-btn" onclick="setArticleSort('name')">A–Z</button></div></div><div id="articles-list"></div></div>
<div id="merge-sources-panel"><h3>Saved Sources</h3><div id="merge-sources-list"></div></div>
<input type="file" id="merge-source-file" style="display:none" onchange="importMergeSourceFile(this)">
`

// techwriterJS is the app's main JS, appended after webui.BaseJS().
const techwriterJS = `
var currentId = null;
var revisions = [];
var revisionIndex = -1;

function loadRevisions(articleID) {
  if (!articleID) { revisions = []; revisionIndex = -1; updateRevNav(); return; }
  fetch('/api/revisions/' + encodeURIComponent(articleID))
    .then(function(r) { return r.json(); })
    .then(function(data) {
      revisions = data || [];
      revisionIndex = revisions.length - 1;
      updateRevNav();
    }).catch(function() { revisions = []; revisionIndex = -1; updateRevNav(); });
}

function updateRevNav() {
  var back = document.getElementById('rev-back');
  var fwd = document.getElementById('rev-fwd');
  var ind = document.getElementById('rev-indicator');
  var mark = document.getElementById('rev-mark-latest');
  if (!back) return;
  var n = revisions.length;
  back.disabled = revisionIndex <= 0;
  fwd.disabled = revisionIndex >= n - 1;
  ind.textContent = n > 0 ? 'rev ' + (revisionIndex + 1) + '/' + n : '';
  mark.style.display = (n > 0 && revisionIndex < n - 1) ? '' : 'none';
}

function navigateRevision(dir) {
  var idx = revisionIndex + dir;
  if (idx < 0 || idx >= revisions.length) return;
  fetch('/api/revision/' + encodeURIComponent(revisions[idx].id))
    .then(function(r) { if (!r.ok) throw new Error('load'); return r.json(); })
    .then(function(rev) {
      document.getElementById('editor').value = rev.body || '';
      document.getElementById('subject').value = rev.subject || '';
      revisionIndex = idx;
      updateRevNav();
    }).catch(function(err) { addChatMsg('assistant', 'Could not load revision: ' + err.message); });
}

function markAsLatest() {
  autoSave();
}

function autoSave() {
  var body = document.getElementById('editor').value;
  if (!body.trim()) return;
  var subject = document.getElementById('subject').value.trim();
  if (subject) {
    doAutoSave(subject, body);
  } else {
    fetch('/api/suggest-title', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({body: body.slice(0, 2000)})
    }).then(function(r) { return r.json(); })
      .then(function(data) {
        var title = (data.subject || '').trim();
        if (title) document.getElementById('subject').value = title;
        doAutoSave(title || 'Untitled', body);
      }).catch(function() { doAutoSave('Untitled', body); });
  }
}

function doAutoSave(subject, body) {
  fetch('/api/save', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: currentId || '', subject: subject, body: body})
  }).then(function(r) { return r.json(); })
    .then(function(data) {
      if (data.id) {
        currentId = data.id;
        loadRevisions(currentId);
      }
    }).catch(function() {});
}

function startChatResize(e) {
  editorStartResize(e, 'col', {
    target: document.getElementById('chat-pane'),
    container: document.getElementById('main'),
    resizer: document.getElementById('chat-resizer'),
    min: 240, pad: 200
  });
}

// --- Persona Management ---

function loadPersonas() {
  fetch('/api/personas').then(function(r) { return r.json(); }).then(function(personas) {
    var sel = document.getElementById('persona-select');
    // Clear existing options except "Default Style".
    while (sel.options.length > 1) sel.remove(1);
    if (personas && personas.length > 0) {
      for (var i = 0; i < personas.length; i++) {
        var opt = document.createElement('option');
        opt.value = personas[i].name;
        opt.textContent = personas[i].name;
        sel.appendChild(opt);
      }
    }
    // Always show manage option.
    var manage = document.createElement('option');
    manage.value = '__manage__';
    manage.textContent = '— Manage Styles —';
    manage.style.color = '#8b949e';
    sel.appendChild(manage);
  }).catch(function() {});
}

function onPersonaChange() {
  var name = document.getElementById('persona-select').value;
  if (name === '__manage__') {
    document.getElementById('persona-select').value = '';
    showPersonaManager();
    return;
  }
  if (name) {
    addChatMsg('assistant', 'Template <strong>' + escapeHtml(name) + '</strong> selected. All content will follow this writing style.');
  }
}

function showPersonaManager() {
  fetch('/api/personas').then(function(r) { return r.json(); }).then(function(personas) {
    var html = '<div style="margin-bottom:0.5rem"><strong>Personas:</strong></div>';
    html += '<div style="display:flex;align-items:center;gap:0.5rem;margin:0.3rem 0;padding:0.3rem 0.5rem;background:#161b22;border-radius:4px;border:1px solid #30363d">'
      + '<span style="flex:1;color:#c9d1d9">Default Style <span style="color:#8b949e;font-size:0.8rem">(built-in TechWriter prompt)</span></span>'
      + '<button onclick="editDefaultPrompt()" style="padding:0.2rem 0.5rem;background:#21262d;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;cursor:pointer;font-size:0.75rem">Edit</button>'
      + '</div>';
    if (!personas || personas.length === 0) {
      html += '<div style="color:#8b949e;margin-top:0.5rem;font-size:0.85rem">No custom personas yet. Click + Persona to create one.</div>';
      addChatMsg('assistant', html);
      return;
    }
    for (var i = 0; i < personas.length; i++) {
      html += '<div style="display:flex;align-items:center;gap:0.5rem;margin:0.3rem 0;padding:0.3rem 0.5rem;background:#161b22;border-radius:4px">'
        + '<span style="flex:1;color:#c9d1d9">' + escapeHtml(personas[i].name) + ' <span style="color:#8b949e;font-size:0.8rem">' + escapeHtml(personas[i].description || '') + '</span></span>'
        + '<button onclick="editPersona(\'' + escapeHtml(personas[i].name) + '\')" style="padding:0.2rem 0.5rem;background:#21262d;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;cursor:pointer;font-size:0.75rem">Edit</button> '
        + '<button onclick="deletePersona(\'' + escapeHtml(personas[i].name) + '\', this)" style="padding:0.2rem 0.5rem;background:#da3633;border:none;border-radius:4px;color:#fff;cursor:pointer;font-size:0.75rem">Delete</button>'
        + '</div>';
    }
    addChatMsg('assistant', html);
  }).catch(function() {});
}

function editPersona(name) {
  fetch('/api/persona/' + encodeURIComponent(name)).then(function(r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function(data) {
    enterPersonaEditMode(data.structure || '');
    document.getElementById('subject').value = name;
  }).catch(function(err) {
    addChatMsg('assistant', 'Failed to load persona: ' + err.message);
  });
}

function deletePersona(name, btn) {
  if (!confirm('Delete persona "' + name + '"?')) return;
  fetch('/api/persona/' + encodeURIComponent(name), {method: 'DELETE'}).then(function(r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    var row = btn.closest('div');
    if (row) row.remove();
    loadPersonas();
    addChatMsg('assistant', 'Template <strong>' + escapeHtml(name) + '</strong> deleted.');
  }).catch(function(err) {
    addChatMsg('assistant', 'Delete failed: ' + err.message);
  });
}

function getSelectedPersona() {
  var sel = document.getElementById('persona-select');
  return sel ? sel.value : '';
}

var personaEditMode = false;

function uploadPersona(input) {
  var file = input.files[0];
  if (!file) return;
  input.value = '';

  addChatMsg('user', 'Analyzing writing style from: ' + file.name);
  addChatMsg('assistant', '<span class="spinner"></span> Analyzing writing style with vision... (this may take a minute)');

  var form = new FormData();
  form.append('file', file);

  fetch('/api/upload-persona', {
    method: 'POST',
    body: form
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    var msgs = document.getElementById('chat-messages');
    if (msgs.lastChild) msgs.removeChild(msgs.lastChild);
    enterPersonaEditMode(data.structure);
  }).catch(function(err) {
    var msgs = document.getElementById('chat-messages');
    if (msgs.lastChild) msgs.removeChild(msgs.lastChild);
    addChatMsg('assistant', 'Writing style analysis failed: ' + err.message);
  });
}

function enterPersonaEditMode(structure) {
  personaEditMode = true;
  document.getElementById('editor').value = structure || '';
  document.getElementById('subject').value = '';
  document.getElementById('subject').placeholder = 'Persona name...';
  // Show save-template button, change Process to work on template.
  var btn = document.getElementById('fill-gaps-btn');
  if (btn) { btn.dataset.origLabel = btn.textContent; btn.textContent = 'Save Persona'; btn.onclick = savePersonaFromEditor; }
  addChatMsg('assistant', 'Persona loaded into the editor. You can now:\n- Edit it directly in the editor\n- Chat to refine it ("remove the troubleshooting section", "add a prerequisites block")\n- Click <strong>Save Persona</strong> when you\'re happy with it\n- Click <strong>Cancel</strong> to exit template editing');
  // Add cancel button.
  var toolbar = document.getElementById('toolbar');
  var cancel = document.createElement('button');
  cancel.className = 'secondary';
  cancel.id = 'cancel-persona-edit';
  cancel.textContent = 'Cancel';
  cancel.onclick = exitPersonaEditMode;
  toolbar.appendChild(cancel);
}

function exitPersonaEditMode() {
  personaEditMode = false;
  document.getElementById('editor').value = '';
  document.getElementById('subject').value = '';
  document.getElementById('subject').placeholder = 'Article subject...';
  var btn = document.getElementById('fill-gaps-btn');
  if (btn) { btn.textContent = btn.dataset.origLabel || 'Process'; btn.onclick = fillGaps; }
  var cancel = document.getElementById('cancel-persona-edit');
  if (cancel) cancel.remove();
  addChatMsg('assistant', 'Persona editing cancelled.');
}

function savePersonaFromEditor() {
  var name = document.getElementById('subject').value.trim();
  var structure = document.getElementById('editor').value.trim();
  if (!name) { alert('Enter a persona name in the subject field.'); return; }
  if (!structure) { alert('Persona is empty.'); return; }

  fetch('/api/save-persona', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: name, style: structure})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function() {
    addChatMsg('assistant', 'Template <strong>' + escapeHtml(name) + '</strong> saved.');
    loadPersonas();
    exitPersonaEditMode();
  }).catch(function(err) {
    addChatMsg('assistant', 'Save failed: ' + err.message);
  });
}

function showNewPersonaMenu() {
  var hasContent = document.getElementById('editor').value.trim().length > 0;
  var html = '<div style="margin-bottom:0.5rem"><strong>Add Persona:</strong></div>'
    + '<button onclick="document.getElementById(\'persona-file\').click();this.closest(\'.chat-msg\').remove()" style="padding:0.4rem 1rem;background:#21262d;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;cursor:pointer;margin-right:0.5rem">Upload PDF</button>'
    + '<button onclick="this.closest(\'.chat-msg\').remove();createManualPersona()" style="padding:0.4rem 1rem;background:#21262d;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;cursor:pointer;margin-right:0.5rem">Write Manually</button>';
  if (hasContent) {
    html += '<button onclick="this.closest(\'.chat-msg\').remove();saveEditorAsPersona()" style="padding:0.4rem 1rem;background:#238636;border:none;border-radius:6px;color:#fff;cursor:pointer">Save Editor as Persona</button>';
  }
  addChatMsg('assistant', html);
}

function saveEditorAsPersona() {
  var name = document.getElementById('subject').value.trim();
  if (!name) name = prompt('Persona name:');
  if (!name) return;
  var structure = document.getElementById('editor').value.trim();
  if (!structure) { alert('Editor is empty.'); return; }
  fetch('/api/save-persona', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: name, style: structure})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function() {
    addChatMsg('assistant', 'Template <strong>' + escapeHtml(name) + '</strong> saved from editor.');
    loadPersonas();
  }).catch(function(err) {
    addChatMsg('assistant', 'Save failed: ' + err.message);
  });
}

function createManualPersona() {
  enterPersonaEditMode('');
  document.getElementById('editor').placeholder = 'Describe the writing style...\n\nYou can write:\n- A structural skeleton with ## headings and [placeholder] instructions\n- Natural language rules ("use these sections, this tone, always include rollback")\n- Or a mix of both\n\nUse the chat to ask for help building the template.';
}

function previewForCopy() {
  var body = document.getElementById('editor').value.trim();
  var subject = document.getElementById('subject').value.trim();
  if (!body) { alert('Article is empty.'); return; }
  fetch('/api/preview', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({subject: subject, body: body})
  }).then(function(r) { return r.text(); }).then(function(html) {
    var win = window.open('', '_blank');
    win.document.write(html);
    win.document.close();
  }).catch(function(err) {
    addChatMsg('assistant', 'Preview failed: ' + err.message);
  });
}

function editDefaultPrompt() {
  fetch('/api/prompt').then(function(r) { return r.json(); }).then(function(data) {
    var html = '<div style="margin-bottom:0.5rem"><strong>Default Writing Style:</strong>'
      + (data.is_default ? ' <span style="color:#8b949e;font-size:0.8rem">(built-in)</span>' : ' <span style="color:#e3b341;font-size:0.8rem">(customized)</span>')
      + '</div>'
      + '<textarea id="prompt-editor" style="width:100%;height:250px;padding:0.4rem;background:#161b22;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;font-size:0.85rem;line-height:1.4">' + escapeHtml(data.prompt) + '</textarea>'
      + '<div style="margin-top:0.5rem">'
      + '<button onclick="saveDefaultPrompt()" style="padding:0.4rem 1rem;background:#238636;border:none;border-radius:6px;color:#fff;cursor:pointer">Save</button>'
      + ' <button onclick="revertDefaultPrompt()" style="padding:0.4rem 1rem;background:#da3633;border:none;border-radius:6px;color:#fff;cursor:pointer">Revert to Built-in</button>'
      + ' <button onclick="this.closest(\'.chat-msg\').remove()" style="padding:0.4rem 1rem;background:#21262d;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;cursor:pointer">Cancel</button>'
      + '</div>';
    addChatMsg('assistant', html);
  }).catch(function(err) {
    addChatMsg('assistant', 'Failed to load prompt: ' + err.message);
  });
}

function saveDefaultPrompt() {
  var prompt = document.getElementById('prompt-editor').value.trim();
  if (!prompt) { alert('Prompt cannot be empty.'); return; }
  fetch('/api/prompt', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({prompt: prompt})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function() {
    addChatMsg('assistant', 'Default style updated.');
  }).catch(function(err) {
    addChatMsg('assistant', 'Save failed: ' + err.message);
  });
}

function revertDefaultPrompt() {
  if (!confirm('Revert to the built-in TechWriter default?')) return;
  fetch('/api/prompt', {method: 'DELETE'}).then(function(r) {
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function() {
    addChatMsg('assistant', 'Default style reverted to built-in.');
  }).catch(function(err) {
    addChatMsg('assistant', 'Revert failed: ' + err.message);
  });
}

// Load personas on startup.
loadPersonas();

function toggleMergeSource() {
  var pane = document.getElementById('merge-source-pane');
  var arr = document.getElementById('merge-arr');
  pane.classList.toggle('open');
  arr.classList.toggle('open');
  if (pane.classList.contains('open')) {
    document.getElementById('merge-source-input').focus();
  }
}

function openMergeSource() {
  var pane = document.getElementById('merge-source-pane');
  var arr = document.getElementById('merge-arr');
  if (!pane.classList.contains('open')) {
    pane.classList.add('open');
    arr.classList.add('open');
  }
  document.getElementById('merge-source-input').focus();
}

function startMergeSourceResize(e) {
  editorStartResize(e, 'row', {
    target: document.getElementById('merge-source-pane'),
    container: document.getElementById('editor-pane'),
    resizer: document.getElementById('merge-source-resizer'),
    min: 80, pad: 80
  });
}

var currentMergeSourceId = null;
var currentMergeSourceName = null;

function setCurrentMergeSource(id, name) {
  currentMergeSourceId = id;
  currentMergeSourceName = name;
  var el = document.getElementById('merge-source-current');
  if (el) el.textContent = name ? '[' + name + ']' : '';
}

function saveMergeSource() {
  var body = document.getElementById('merge-source-input').value;
  if (!body.trim()) { alert('Merge source is empty.'); return; }
  var name = prompt('Name this source:', currentMergeSourceName || '');
  if (!name) return;
  name = name.trim();
  if (!name) return;
  fetch('/api/merge-sources', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: currentMergeSourceId, name: name, body: body})
  }).then(function(r) { return r.json(); }).then(function(rec) {
    setCurrentMergeSource(rec.id, rec.name);
    addChatMsg('assistant', 'Merge source <strong>' + escapeHtml(name) + '</strong> saved.');
  }).catch(function() { alert('Save failed.'); });
}

function showMergeSources() {
  fetch('/api/merge-sources').then(function(r) { return r.json(); }).then(function(items) {
    document.getElementById('overlay').style.display = 'block';
    document.getElementById('merge-sources-panel').style.display = 'block';
    var list = document.getElementById('merge-sources-list');
    if (!items || items.length === 0) {
      list.innerHTML = '<div style="color:#8b949e;text-align:center;padding:0.5rem">No saved sources.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      var date = '';
      try { date = new Date(item.date).toLocaleDateString(); } catch(e) {}
      html += '<div class="merge-source-item" onclick="loadMergeSource(\'' + item.id + '\')">';
      html += '<div><div class="title">' + escapeHtml(item.name || 'Untitled') + '</div><div class="date">' + date + '</div></div>';
      html += '<button class="del-btn" onclick="event.stopPropagation();deleteMergeSource(\'' + item.id + '\',this.closest(\'.merge-source-item\'))">&times;</button>';
      html += '</div>';
    }
    list.innerHTML = html;
  });
}

function loadMergeSource(id) {
  fetch('/api/merge-source/' + encodeURIComponent(id))
    .then(function(r) { if (!r.ok) throw new Error('load'); return r.json(); })
    .then(function(rec) {
      document.getElementById('merge-source-input').value = rec.body || '';
      setCurrentMergeSource(rec.id, rec.name);
      openMergeSource();
      hideArticles();
    }).catch(function() { alert('Load failed.'); });
}

function deleteMergeSource(id, el) {
  if (!confirm('Delete this saved source?')) return;
  fetch('/api/merge-source/' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function() {
      if (currentMergeSourceId === id) setCurrentMergeSource(null, null);
      if (el) el.remove();
    });
}

function importMergeSource() {
  document.getElementById('merge-source-file').click();
}

function importMergeSourceFile(input) {
  if (!input.files || !input.files[0]) return;
  var file = input.files[0];
  var reader = new FileReader();
  reader.onload = function(e) {
    document.getElementById('merge-source-input').value = e.target.result;
    setCurrentMergeSource(null, file.name.replace(/\.[^.]+$/, ''));
    openMergeSource();
  };
  reader.readAsText(file);
  input.value = '';
}

// showMergeDialog kept as alias so any bookmarks/external calls still work.
function showMergeDialog() { toggleMergeSource(); }

function runMerge() {
  var subject = document.getElementById('subject').value.trim();
  var body = document.getElementById('editor').value.trim();
  var other = document.getElementById('merge-source-input').value.trim();
  var guidance = document.getElementById('merge-guidance-inline').value.trim();
  if (!other) { alert('Paste content into the Merge Source panel first.'); return; }
  if (!body) { alert('Current article is empty.'); return; }

  var btn = document.getElementById('merge-run');
  btn.disabled = true;
  btn.textContent = 'Merging...';

  addChatMsg('user', '[Merge with pasted content]');

  fetch('/api/merge', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({subject: subject, body: body, other: other, mode: 'docs', guidance: guidance})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    btn.disabled = false;
    btn.textContent = 'Merge';
    var titleNote = data.title ? ' New title: <strong>' + escapeHtml(data.title) + '</strong>' : '';
    var mergeCurrent = document.getElementById('editor').value || '';
    var mergeStats = editorDiffStats(mergeCurrent, data.content);
    var mergeHtml = 'Merged.' + titleNote +
      '<div class="editor-diff-actions">' +
      '<span class="editor-diff-summary">Proposed changes ready in editor (+' +
      mergeStats.add + ' -' + mergeStats.remove + ')</span>' +
      '</div>';
    var mergeTitle = data.title || '';
    editorShowDiff({
      newText: data.content,
      onApply: function(text) {
        document.getElementById('editor').value = text;
        if (mergeTitle) document.getElementById('subject').value = mergeTitle;
        autoSave();
      }
    });
    addChatMsg('assistant', mergeHtml, data.content, mergeTitle);
  }).catch(function(err) {
    btn.disabled = false;
    btn.textContent = 'Merge';
    addChatMsg('assistant', 'Merge failed: ' + err.message);
  });
}

// chatHistory keeps the running conversation across turns and across
// Chat/Edit mode switches. Article state (body, subject) is NOT stored
// here — only the discussion. Cleared on article load and when the
// Clear button is pressed.
var chatHistory = [];

function appendHistory(role, content) {
  if (!content) return;
  chatHistory.push({role: role, content: content});
  if (chatHistory.length > 40) {
    chatHistory = chatHistory.slice(-40);
  }
}

function clearChatHistory() {
  chatHistory = [];
}

function sendChat(mode) {
  // mode: 'chat' (discuss only, never rewrite the article) or 'edit'
  // (propose a rewrite). Defaults to 'edit' for back-compat.
  mode = mode === 'chat' ? 'chat' : 'edit';
  var input = document.getElementById('chat-input');
  var msg = input.value.trim();
  if (!msg) return;
  input.value = '';
  var prefix = mode === 'chat' ? '<span class="mode-tag">chat</span> ' : '';
  addChatMsg('user', prefix + msg);
  appendHistory('user', msg);
  chatAPI(msg, mode);
}

var fillGapsRunning = false;
function fillGaps() {
  if (fillGapsRunning) return;
  var body = document.getElementById('editor').value.trim();
  if (!body) return;
  fillGapsRunning = true;
  var btn = document.getElementById('fill-gaps-btn');
  if (btn) {
    btn.disabled = true;
    btn.dataset.label = btn.textContent;
    btn.innerHTML = '<span class="spinner"></span>Processing...';
  }
  addChatMsg('user', '[Process]');
  chatAPI('Fill in the gaps in this article. Expand any bare commands with explanations, add missing context, flesh out incomplete steps, and ensure the article flows well. Preserve all existing commands and content exactly. Return the complete updated article.');
}

function fillGapsDone() {
  fillGapsRunning = false;
  var btn = document.getElementById('fill-gaps-btn');
  if (btn) {
    btn.disabled = false;
    if (personaEditMode) {
      btn.textContent = 'Save Persona';
    } else {
      btn.textContent = btn.dataset.label || 'Process';
    }
  }
}

function chatAPI(message, mode) {
  // Default to 'edit' so non-sendChat callers (e.g. fillGaps) keep the
  // legacy rewrite-proposing behavior. Only sendChat ever passes 'chat'.
  mode = mode === 'chat' ? 'chat' : 'edit';
  var subject = document.getElementById('subject').value.trim();
  var body = document.getElementById('editor').value;
  var persona = getSelectedPersona();
  document.getElementById('chat-send').disabled = true;
  var talkBtn = document.getElementById('chat-talk');
  if (talkBtn) talkBtn.disabled = true;
  addChatMsg('assistant', '<span class="spinner"></span> Thinking...');
  var thinkingMsg = document.getElementById('chat-messages').lastChild;

  fetch('/api/chat', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      subject: subject, body: body, message: message, mode: mode,
      persona: persona, persona_edit: personaEditMode,
      // Exclude the just-pushed user message so the server sees it
      // only once (as the current request), not also as history.
      history: chatHistory.slice(0, -1)
    })
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
    return r.json();
  }).then(function(data) {
    // Server may return a 200 with {"error": "..."} when the heartbeat
    // already committed headers and a later LLM error couldn't switch
    // to a 500 status. Surface it through the same error UI.
    if (data && data.error) throw new Error(data.error);
    if (thinkingMsg) thinkingMsg.remove();
    document.getElementById('chat-send').disabled = false;
    if (talkBtn) talkBtn.disabled = false;
    fillGapsDone();
    // Client-side guard: in chat mode, never process an article response
    // even if one sneaks through. The server already re-stamps to type=chat,
    // but this keeps the UI honest.
    if (mode !== 'chat' && data.type === 'article') {
      if (data.image_url) window._blogImageURL = data.image_url;
      var html = 'Article updated.';

      // Title picker — show options if multiple.
      if (data.titles && data.titles.length > 1) {
        html += '<div style="margin:0.5rem 0"><strong>Choose a title:</strong></div>';
        for (var ti = 0; ti < data.titles.length; ti++) {
          var sel = ti === 0 ? 'background:#238636;color:#fff' : 'background:#21262d;color:#c9d1d9;border:1px solid #30363d';
          html += '<button class="title-opt" data-idx="' + ti + '" onclick="pickTitle(this)" style="display:block;width:100%;text-align:left;padding:0.4rem 0.6rem;margin:0.25rem 0;border-radius:4px;cursor:pointer;border:none;font-size:0.85rem;' + sel + '">' + escapeHtml(data.titles[ti]) + '</button>';
        }
      } else if (data.title) {
        html += ' Title: <strong>' + escapeHtml(data.title) + '</strong>';
      }

      // Image picker — show thumbnails if multiple.
      if (data.images && data.images.length > 0) {
        html += '<div style="margin:0.5rem 0"><strong>Choose a header image:</strong></div><div style="display:flex;gap:0.5rem;flex-wrap:wrap">';
        for (var ii = 0; ii < data.images.length; ii++) {
          var border = ii === 0 ? '2px solid #238636' : '2px solid #30363d';
          html += '<img class="img-opt" data-idx="' + ii + '" data-url="' + escapeHtml(data.images[ii].url) + '" onclick="pickImage(this)" src="' + escapeHtml(data.images[ii].url) + '" style="width:180px;height:100px;object-fit:cover;border-radius:6px;cursor:pointer;border:' + border + '">';
        }
        html += '</div>';
      }

      // In template edit mode, auto-apply so the editor always has the latest version.
      if (personaEditMode) {
        document.getElementById('editor').value = data.content;
        document.getElementById('subject').placeholder = 'Persona name...';
        addChatMsg('assistant', 'Persona updated in editor. Click <strong>Save Persona</strong> when ready.');
      } else {
        var chatCurrent = document.getElementById('editor').value || '';
        var chatStats = editorDiffStats(chatCurrent, data.content);
        html += '<div class="editor-diff-actions">' +
          '<span class="editor-diff-summary">Proposed changes ready in editor (+' +
          chatStats.add + ' -' + chatStats.remove + ')</span>' +
          '</div>';
        var chatTitle = (data.titles && data.titles[0]) || data.title || '';
        editorShowDiff({
          newText: data.content,
          onApply: function(text) {
            document.getElementById('editor').value = text;
            if (chatTitle) document.getElementById('subject').value = chatTitle;
            autoSave();
          }
        });
        addChatMsg('assistant', html, data.content, chatTitle);
      }
    } else {
      addChatMsg('assistant', formatChat(data.content));
    }
    // Record the assistant's reply for subsequent turns so Edit can
    // reference what was just said in Chat. For article-type replies,
    // store the content too — the LLM will then have the full text it
    // proposed if the user follows up with "tweak the intro."
    appendHistory('assistant', data.content || '');
  }).catch(function(err) {
    if (thinkingMsg) thinkingMsg.remove();
    document.getElementById('chat-send').disabled = false;
    if (talkBtn) talkBtn.disabled = false;
    fillGapsDone();
    addChatMsg('assistant', 'Error: ' + err.message);
  });
}

function addChatMsg(role, html, articleData, titleData) {
  var div = document.createElement('div');
  div.className = 'chat-msg ' + role;
  div.innerHTML = html;
  if (articleData) div.dataset.article = articleData;
  if (titleData) div.dataset.title = titleData;
  var msgs = document.getElementById('chat-messages');
  msgs.appendChild(div);
  msgs.scrollTop = msgs.scrollHeight;
}

function pickTitle(btn) {
  var msg = btn.closest('.chat-msg');
  msg.querySelectorAll('.title-opt').forEach(function(b) {
    b.style.background = '#21262d';
    b.style.color = '#c9d1d9';
    b.style.border = '1px solid #30363d';
  });
  btn.style.background = '#238636';
  btn.style.color = '#fff';
  btn.style.border = 'none';
  msg.dataset.title = btn.textContent;
}

function pickImage(img) {
  var msg = img.closest('.chat-msg');
  msg.querySelectorAll('.img-opt').forEach(function(i) {
    i.style.border = '2px solid #30363d';
  });
  img.style.border = '2px solid #238636';
  window._blogImageURL = img.dataset.url;
}

function formatChat(text) {
  var bt = String.fromCharCode(96);
  text = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  text = text.replace(new RegExp(bt+bt+bt+'(\\w*)\\n([\\s\\S]*?)'+bt+bt+bt, 'g'), '<pre><code>$2</code></pre>');
  text = text.replace(new RegExp(bt+'([^'+bt+']+)'+bt, 'g'), '<code>$1</code>');
  text = text.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  text = text.replace(/\n/g, '<br>');
  return text;
}

function saveArticle() {
  var subject = document.getElementById('subject').value.trim();
  var body = document.getElementById('editor').value;
  if (!subject) { alert('Enter a subject first.'); return; }

  fetch('/api/save', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({id: currentId || '', subject: subject, body: body})
  }).then(function(r) { return r.json(); }).then(function(data) {
    currentId = data.id;
    addChatMsg('assistant', 'Article saved.');
    loadRevisions(currentId);
  });
}

function newArticle() {
  if (personaEditMode) return; // stay in persona mode — use Cancel to exit
  currentId = null;
  revisions = []; revisionIndex = -1; updateRevNav();
  document.getElementById('subject').value = '';
  document.getElementById('editor').value = '';
  document.getElementById('chat-messages').innerHTML = '';
  clearChatHistory();
}

function resetPage() {
  if (!confirm('Reset everything? This will clear the editor, chat, and any unsaved work.')) return;
  window.location.href = window.location.pathname;
}

function exportArticle() {
  if (!currentId) { alert('Save the article first.'); return; }
  window.open('/api/export/' + currentId);
}

var articleSort = 'date';
var articleItems = [];

function setArticleSort(s) {
  articleSort = s;
  document.getElementById('art-sort-date').className = 'art-sort-btn' + (s === 'date' ? ' active' : '');
  document.getElementById('art-sort-name').className = 'art-sort-btn' + (s === 'name' ? ' active' : '');
  renderArticles();
}

function renderArticles() {
  var list = document.getElementById('articles-list');
  if (!articleItems || articleItems.length === 0) {
    list.innerHTML = '<div style="color:#8b949e;text-align:center;padding:0.5rem">No saved articles.</div>';
    return;
  }
  var sorted = articleItems.slice();
  if (articleSort === 'name') {
    sorted.sort(function(a, b) { return (a.Subject || '').localeCompare(b.Subject || ''); });
  } else {
    sorted.sort(function(a, b) { return (b.Date || '') < (a.Date || '') ? -1 : 1; });
  }
  var html = '';
  for (var i = 0; i < sorted.length; i++) {
    var item = sorted[i];
    var date = '';
    try { date = new Date(item.Date).toLocaleDateString(); } catch(e) {}
    html += '<div class="article-item" onclick="loadArticle(\'' + item.ID + '\')">';
    html += '<div><div class="title">' + escapeHtml(item.Subject || 'Untitled') + '</div><div class="date">' + date + '</div></div>';
    html += '<button class="del-btn" onclick="event.stopPropagation();deleteArticle(\'' + item.ID + '\',this.closest(\'.article-item\'))">&times;</button>';
    html += '</div>';
  }
  list.innerHTML = html;
}

function showArticles() {
  document.getElementById('overlay').style.display = 'block';
  document.getElementById('articles-panel').style.display = 'block';
  var list = document.getElementById('articles-list');
  list.innerHTML = '<div style="color:#8b949e;text-align:center;padding:0.5rem">Loading...</div>';
  fetch('/api/list').then(function(r) { return r.json(); }).then(function(items) {
    articleItems = items || [];
    renderArticles();
  });
}

function hideArticles() {
  document.getElementById('overlay').style.display = 'none';
  document.getElementById('articles-panel').style.display = 'none';
  document.getElementById('merge-sources-panel').style.display = 'none';
  document.getElementById('diagram-modal').classList.remove('open');
}

function loadArticle(id) {
  hideArticles();
  return fetch('/api/load/' + id).then(function(r) { return r.json(); }).then(function(rec) {
    currentId = rec.ID;
    document.getElementById('subject').value = rec.Subject || '';
    document.getElementById('editor').value = rec.Body || '';
    document.getElementById('chat-messages').innerHTML = '';
    clearChatHistory();
    loadRevisions(currentId);
  });
}

function deleteArticle(id, el) {
  fetch('/api/delete/' + id, {method: 'DELETE'}).then(function() {
    articleItems = articleItems.filter(function(a) { return a.ID !== id; });
    if (el) el.remove();
    if (currentId === id) newArticle();
  });
}

function importArticle(input) {
  if (!input.files || !input.files[0]) return;
  var form = new FormData();
  form.append('file', input.files[0]);
  fetch('/api/import', {method: 'POST', body: form})
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if (data.id) loadArticle(data.id);
    })
    .catch(function(err) { alert('Import failed: ' + err.message); });
  input.value = '';
}

function escapeHtml(s) {
  var d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

// Auto-load article from URL param (e.g. ?article={id}).
(function() {
  var params = new URLSearchParams(window.location.search);
  var articleId = params.get('article');
  if (articleId) {
    window.addEventListener('DOMContentLoaded', function() {
      loadArticle(articleId);
    });
  }
})();

// --- Diagram Tool ---

var mermaidLoaded = false;

function openDiagramTool() {
  document.getElementById('diagram-modal').classList.add('open');
  document.getElementById('overlay').style.display = 'block';
}

function closeDiagramTool() {
  document.getElementById('diagram-modal').classList.remove('open');
  document.getElementById('overlay').style.display = 'none';
}

function scanForMermaid() {
  var body = document.getElementById('editor').value;
  var blocks = [];
  var re = /` + "```" + `mermaid\n([\s\S]*?)` + "```" + `/g;
  var m;
  while ((m = re.exec(body)) !== null) {
    blocks.push(m[1].trim());
  }
  var picker = document.getElementById('diagram-picker');
  if (blocks.length === 0) {
    picker.innerHTML = '<div style="color:#8b949e;font-size:0.78rem">No Mermaid diagrams found in the document.</div>';
    return;
  }
  if (blocks.length === 1) {
    picker.innerHTML = '';
    document.getElementById('diagram-source').value = blocks[0];
    renderDiagramPreview();
    return;
  }
  picker.innerHTML = '<div style="color:#8b949e;font-size:0.72rem;margin-bottom:0.2rem">' + blocks.length + ' diagrams found — pick one:</div>';
  blocks.forEach(function(block, i) {
    var btn = document.createElement('button');
    btn.className = 'diagram-pick-btn';
    var first = block.split('\n')[0] || '';
    btn.textContent = 'Diagram ' + (i + 1) + (first ? ': ' + first : '');
    btn.title = block.substring(0, 120);
    btn.onclick = function() {
      document.getElementById('diagram-source').value = block;
      renderDiagramPreview();
    };
    picker.appendChild(btn);
  });
}

function loadMermaid(cb) {
  if (mermaidLoaded) { cb(); return; }
  var s = document.createElement('script');
  s.src = 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.min.js';
  s.onload = function() {
    mermaid.initialize({ startOnLoad: false, theme: 'neutral' });
    mermaidLoaded = true;
    cb();
  };
  s.onerror = function() {
    document.getElementById('diagram-preview').innerHTML =
      '<span style="color:#f85149;font-size:0.85rem">Failed to load Mermaid.js — check your connection.</span>';
  };
  document.head.appendChild(s);
}

// fixMermaidZOrder moves .cluster-label elements (subgraph titles) to paint
// after .nodes so they are never hidden behind node rectangles. Mermaid renders
// clusters before nodes in the SVG, meaning node shapes cover subgraph titles.
// We clone each label with its absolute SVG position and re-append to the root.
function fixMermaidZOrder(svg) {
  if (!svg) return;
  var root = svg.querySelector('g.root') || svg;
  var ctm = svg.getScreenCTM && svg.getScreenCTM();
  var inv = ctm && ctm.inverse();
  if (!inv) return;
  Array.from(root.querySelectorAll('.cluster-label')).forEach(function(label) {
    var r = label.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return;
    var pt = svg.createSVGPoint();
    pt.x = r.left; pt.y = r.top;
    var sp = pt.matrixTransform(inv);
    var clone = label.cloneNode(true);
    clone.setAttribute('transform', 'translate(' + sp.x + ',' + sp.y + ')');
    label.style.visibility = 'hidden';
    root.appendChild(clone);
  });
}

function renderDiagramPreview() {
  var src = document.getElementById('diagram-source').value.trim();
  if (!src) return;
  var preview = document.getElementById('diagram-preview');
  preview.innerHTML = '<span class="spinner"></span><span style="color:#8b949e;font-size:0.85rem">Rendering...</span>';
  loadMermaid(function() {
    var id = 'mermaid-' + Date.now();
    mermaid.render(id, src).then(function(result) {
      preview.innerHTML = result.svg;
      fixMermaidZOrder(preview.querySelector('svg'));
    }).catch(function(err) {
      var msg = err && err.message ? err.message : String(err);
      preview.innerHTML = '<span style="color:#f85149;font-size:0.85rem">Render failed: ' + escapeHtml(msg) + '</span>';
    });
  });
}

function diagramSVG() {
  return document.querySelector('#diagram-preview svg');
}

function diagramDims(svg) {
  var vb = svg.viewBox && svg.viewBox.baseVal;
  var w = (vb && vb.width > 0 ? vb.width : svg.clientWidth) || 800;
  var h = (vb && vb.height > 0 ? vb.height : svg.clientHeight) || 600;
  return { w: Math.round(w), h: Math.round(h) };
}

function triggerDownload(href, name) {
  var a = document.createElement('a');
  a.href = href; a.download = name; a.click();
  if (href.startsWith('blob:')) setTimeout(function() { URL.revokeObjectURL(href); }, 1500);
}

function downloadDiagramSVG() {
  var svg = diagramSVG();
  if (!svg) { alert('Render a diagram first.'); return; }
  var data = new XMLSerializer().serializeToString(svg);
  triggerDownload(URL.createObjectURL(new Blob([data], {type: 'image/svg+xml'})), 'diagram.svg');
}

function downloadDiagramPNG() {
  var svg = diagramSVG();
  if (!svg) { alert('Render a diagram first.'); return; }
  var scale = 2;
  var pad = 16; // padding so edge text isn't clipped by the tight Mermaid viewBox
  var clone = svg.cloneNode(true);
  if (!clone.getAttribute('xmlns')) clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg');
  // Expand the viewBox by pad on every side so labels at the boundary aren't cut off.
  var vb = svg.viewBox && svg.viewBox.baseVal;
  var vx = vb && vb.width > 0 ? vb.x : 0;
  var vy = vb && vb.height > 0 ? vb.y : 0;
  var vw = vb && vb.width > 0 ? vb.width : (svg.clientWidth || 800);
  var vh = vb && vb.height > 0 ? vb.height : (svg.clientHeight || 600);
  clone.setAttribute('viewBox', (vx - pad) + ' ' + (vy - pad) + ' ' + (vw + pad * 2) + ' ' + (vh + pad * 2));
  var w = Math.round(vw + pad * 2);
  var h = Math.round(vh + pad * 2);
  clone.setAttribute('width', w);
  clone.setAttribute('height', h);
  var svgData = new XMLSerializer().serializeToString(clone);
  var dataUrl = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(svgData)));
  var canvas = document.createElement('canvas');
  canvas.width = w * scale; canvas.height = h * scale;
  var ctx = canvas.getContext('2d');
  ctx.scale(scale, scale);
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, w, h);
  var img = new Image();
  img.onload = function() {
    ctx.drawImage(img, 0, 0);
    triggerDownload(canvas.toDataURL('image/png'), 'diagram.png');
  };
  img.onerror = function() { alert('PNG export failed — try SVG instead.'); };
  img.src = dataUrl;
}

function downloadDiagramDrawio() {
  var svg = diagramSVG();
  if (!svg) { alert('Render a diagram first.'); return; }
  var d = diagramDims(svg);
  var svgData = new XMLSerializer().serializeToString(svg);
  var b64 = btoa(unescape(encodeURIComponent(svgData)));
  var xml = '<?xml version="1.0" encoding="UTF-8"?>\n'
    + '<mxGraphModel><root>'
    + '<mxCell id="0"/><mxCell id="1" parent="0"/>'
    + '<mxCell id="2" value="" style="shape=image;verticalLabelPosition=bottom;labelBackgroundColor=default;'
    + 'verticalAlign=top;align=center;strokeColor=none;fillColor=none;'
    + 'image=data:image/svg+xml;base64,' + b64 + ';" vertex="1" parent="1">'
    + '<mxGeometry x="20" y="20" width="' + d.w + '" height="' + d.h + '" as="geometry"/>'
    + '</mxCell></root></mxGraphModel>';
  triggerDownload(URL.createObjectURL(new Blob([xml], {type: 'application/xml'})), 'diagram.drawio');
}
`
