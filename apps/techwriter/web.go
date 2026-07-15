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
func (T *TechWriterAgent) WebAccessKey() string { return "techwriter" }
func (T *TechWriterAgent) WebAccessCheck(r *http.Request) bool {
	return UserHasAppAccess(r, "/techwriter")
}
func (T *TechWriterAgent) WebDesc() string {
	return "Technical article co-writer for documentation and instructions"
}

func (T *TechWriterAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	// Framework-based techwriter at /techwriter/ (core/ui ArticleEditor).
	// The old hand-rolled /techwriter/legacy surface has been retired.
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handleNewTechwriterPage(w, r)
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
	sub.HandleFunc("/api/reference-sources", T.handleReferenceSources)
	sub.HandleFunc("/api/suggest-title", T.handleSuggestTitle)
	sub.HandleFunc("/api/prompt", T.handlePrompt)
	sub.HandleFunc("/api/preview", T.handlePreview)
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

// handleReferenceSources feeds the chat-pane reference picker: every
// registered reference source's items available to this user, grouped. The
// data is generic (core.ReferenceGroup) — techwriter doesn't know or care
// which services contributed (servitor systems, collections, …).
func (T *TechWriterAgent) handleReferenceSources(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	groups := ReferenceGroups(userID)
	if groups == nil {
		groups = []ReferenceGroup{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(groups)
}

func (T *TechWriterAgent) handleChat(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Message string `json:"message"`
		// Mode controls whether the LLM may propose an article rewrite.
		// "edit" (default) = legacy behavior; the LLM may emit an
		// ARTICLE:-prefixed response and the UI offers to apply it.
		// "chat" = discuss / explain / ask questions only; never emit
		// a full article body. Used for "talk about the draft before
		// touching it" conversations. Blank defaults to edit.
		Mode string `json:"mode"`
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
		// References are reference-source selections the user picked in the
		// chat pane ([{kind, item_id}]). Each source's text is fetched via
		// the core reference registry and injected into the system prompt so
		// the draft can be grounded in knowledge gathered by other services.
		References []ReferenceSelection `json:"references"`
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

	// Compose system prompt: base default + per-user Rules. Rules go
	// LAST so they're closest to the user message and weigh heaviest
	// on the model's behavior.
	system_prompt := T.activeDefaultPrompt() +
		loadUserRules(udb)

	if chatOnly {
		// Discussion-only mode overrides any mode-specific rule above
		// (including the PersonaEdit ARTICLE: rule). The user is
		// talking about the draft, not asking for a rewrite, so the
		// LLM must not emit an article body under any label. Applied
		// last so it wins when multiple rule blocks conflict.
		system_prompt += "\n\nDISCUSSION MODE: the user is chatting about the article, not asking for it to be rewritten. Do NOT emit an ARTICLE: prefix. Do NOT return a revised or complete article body. Answer questions, explain your thinking, suggest approaches, or describe the change you'd make — all in conversational prose. If the user says \"make the change\" or similar, tell them to click Edit instead of Chat."
	}

	// Reference context — knowledge gathered by other services (servitor
	// systems, collections, …) that the user selected in the chat pane.
	// Injected last so it sits closest to the user message. Each attached
	// item's own tools (search / facts / live investigate) also ride the
	// chat so the LLM can dig deeper — or run a live investigation — when
	// the cached picture doesn't answer.
	var ref_tools []AgentToolDef
	if len(req.References) > 0 {
		if ref := FetchReferences(r.Context(), userID, req.Message, req.References); ref != "" {
			system_prompt += "\n\n" + ref
		}
		for _, sel := range req.References {
			ref_tools = append(ref_tools, ReferenceItemTools(userID, sel.Kind, sel.ItemID)...)
		}
		if len(ref_tools) > 0 {
			system_prompt += "\n\nThe attached reference source also provides tools. Use them when the reference context above doesn't answer the question, or when the article needs the CURRENT state of the system — the investigate tool runs a live session when cached knowledge isn't enough."
		}
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
		Content: fmt.Sprintf("Today is %s.\n\n%s%s", today, req.Message, article_context),
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
	resp, err := T.AppCore.WorkerChatWithTools(chatCtx, messages, ref_tools,
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
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	// Deletes must not be GET-reachable: a cross-site top-level GET carries the
	// session cookie under SameSite=Lax and would bypass the Origin check (which
	// only covers non-safe methods). The workbench calls this via DELETE.
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if !IsStateChangingMethod(r.Method) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

	// The editor client only applies a merge when the response declares
	// type=article — without it, merged output falls into the
	// "conversational text" branch and is silently never applied.
	result := map[string]string{"type": "article", "content": article}
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
