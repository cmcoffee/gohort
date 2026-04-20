package techwriter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
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
- TABLE OF CONTENTS: If the article has 4 or more ## sections, add a brief table of contents after the opening paragraph using markdown anchor links:
  ## Table of Contents
  - [Prerequisites](#prerequisites)
  - [Configuration](#configuration)
  - [Deployment Steps](#deployment-steps)
  - [Verification](#verification)
  - [Rollback](#rollback)
  The TOC should list every ## heading as a link. Heading text becomes lowercase with spaces replaced by dashes for the anchor.
- When referencing another section of the article, use an anchor link: "See [Rollback](#rollback) if something goes wrong." Do not say "see below" or "see the section above" — link directly.

IMPORTANT: If the article contains source citations like [1], [2], [N] or a ## Sources section, preserve ALL citations exactly as they appear. Keep every [N] reference in the text and keep the Sources section intact.

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
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "TechWriter",
			AppName:  "TechWriter",
			Prefix:   prefix,
			BodyHTML: techwriterBody,
			AppCSS:   editor.DiffCSS() + techwriterCSS,
			AppJS:    editor.DiffJS() + techwriterJS,
		}))
	})
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/save", T.handleSave)
	sub.HandleFunc("/api/list", T.handleList)
	sub.HandleFunc("/api/load/", T.handleLoad)
	sub.HandleFunc("/api/delete/", T.handleDelete)
	sub.HandleFunc("/api/export/", T.handleExport)
	sub.HandleFunc("/api/import", T.handleImport)
	sub.HandleFunc("/api/merge", T.handleMerge)
	sub.HandleFunc("/api/prompt", T.handlePrompt)
	sub.HandleFunc("/api/preview", T.handlePreview)
	sub.HandleFunc("/api/upload-persona", T.handleUploadPersona)
	sub.HandleFunc("/api/save-persona", T.handleSavePersona)
	sub.HandleFunc("/api/personas", T.handleListPersonas)
	sub.HandleFunc("/api/persona/", T.handlePersona)

	RegisterUserDataHandler(&techWriterUserData{agent: T})

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

	system_prompt := T.activeDefaultPrompt() + personaPromptSection(T.loadPersonaStyle(udb, req.Persona))
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

	// TechWriter always uses the local worker LLM.
	agent := &FuzzAgent{LLM: T.FuzzAgent.LLM}

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
	session := agent.CreateSession(WORKER)
	resp, err := session.Chat(r.Context(), messages,
		WithSystemPrompt(system_prompt),
		WithMaxTokens(4096),
		WithThink(false))

	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))
	w.Header().Set("Content-Type", "application/json")

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
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	udb.Set(HistoryTable, req.ID, ArticleRecord{
		ID:      req.ID,
		Subject: req.Subject,
		Body:    req.Body,
		Date:    time.Now().Format(time.RFC3339),
	})
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"%s"}`, req.ID)
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

	agent := &FuzzAgent{LLM: T.FuzzAgent.LLM}
	session := agent.CreateSession(WORKER)
	resp, err := session.Chat(r.Context(), []Message{
		{Role: "user", Content: merge_prompt},
	}, WithSystemPrompt(system_prompt),
		WithMaxTokens(8192),
		WithThink(false))

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
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		http.Error(w, "body required", http.StatusBadRequest)
		return
	}
	html := MarkdownToHTML(req.Body)
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
th, td { border: 1px solid #dfe1e6; padding: 0.5rem 0.8rem; text-align: left; }
th { background: #f4f5f7; font-weight: 600; }
blockquote { border-left: 3px solid #0052cc; background: #deebff; padding: 0.8rem 1rem; margin: 1rem 0; border-radius: 3px; }
ul, ol { padding-left: 1.5rem; }
li { margin: 0.3rem 0; }
.copy-hint { position: fixed; top: 0; left: 0; right: 0; background: #0052cc; color: white; text-align: center; padding: 0.5rem; font-size: 0.85rem; z-index: 100; }
</style></head><body>
<div class="copy-hint">
  <button onclick="copyContent()" style="background:white;color:#0052cc;border:none;border-radius:4px;padding:0.3rem 1rem;cursor:pointer;font-weight:600;margin-right:0.5rem">Copy to Clipboard</button>
  Then paste directly into Confluence editor
</div>
<div id="content">
<h1>%s</h1>
%s
</div>
<script>
function copyContent() {
  var content = document.getElementById('content');
  var range = document.createRange();
  range.selectNodeContents(content);
  var sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  document.execCommand('copy');
  sel.removeAllRanges();
  var btn = document.querySelector('.copy-hint button');
  btn.textContent = 'Copied!';
  setTimeout(function() { btn.textContent = 'Copy to Clipboard'; }, 2000);
}
</script>
</body></html>`, HTMLEscape(title), HTMLEscape(title), html)
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
  table { border-collapse: collapse; width: 100%%; margin: 1rem 0; }
  th, td { border: 1px solid #d0d7de; padding: 0.5rem; text-align: left; }
  th { background: #f6f8fa; }
  .note { background: #ddf4ff; border: 1px solid #54aeff; border-radius: 6px; padding: 0.75rem 1rem; margin: 1rem 0; }
  .warning { background: #fff8c5; border: 1px solid #d4a72c; border-radius: 6px; padding: 0.75rem 1rem; margin: 1rem 0; }
</style>
</head>
<body>
<h1>%s</h1>
<div class="meta"><span>%s</span><span>%d words</span><span>%d min read</span></div>
%s
</body>
</html>`, HTMLEscape(rec.Subject), HTMLEscape(rec.Subject), date, words, read_time, MarkdownToHTML(rec.Body))
}

// Use shared functions from core:
// HTMLEscape, MarkdownToHTML, InlineMarkdownToHTML, HTMLToMarkdown, HTMLUnescape

// techwriterCSS holds techwriter-specific styles. The shared dark theme,
// buttons, scrollbars, and modal primitives come from webui.BaseCSS().
const techwriterCSS = `
  body { height: 100vh; display: flex; flex-direction: column; }
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
    display: flex; gap: 0.5rem; padding: 0.5rem 0.75rem;
    border-top: 1px solid #21262d;
    align-items: flex-end;
  }
  #chat-input {
    flex: 1; background: #0d1117; border: 1px solid #30363d; color: #c9d1d9;
    padding: 0.4rem 0.6rem; border-radius: 6px; font-size: 0.85rem;
    font-family: inherit; resize: vertical; min-height: 38px; max-height: 200px;
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
  #articles-panel h3 { color: #c9d1d9; margin-bottom: 0.75rem; }
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
`

// techwriterBody is the app's main HTML panel, inserted into the
// shared scaffolding by webui.RenderPage.
const techwriterBody = `
<div id="toolbar">
  <span class="app-title">TechWriter</span>
  <button class="secondary" onclick="showArticles()">Articles</button>
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
  <button class="secondary" onclick="document.getElementById('import-file').click()">Import</button>
  <input type="file" id="import-file" accept=".html,.htm" style="display:none" onchange="importArticle(this)">
  <button class="secondary" onclick="showMergeDialog()">Merge</button>
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
  </div>
  <div id="merge-pane" style="display:none;flex-direction:column;flex:1;border-left:1px solid #30363d;background:#0d1117">
    <div style="padding:0.5rem 1rem;background:#161b22;border-bottom:1px solid #30363d;display:flex;align-items:center;gap:0.5rem">
      <strong style="color:#c9d1d9;font-size:0.9rem;flex:1">Merge with pasted content</strong>
      <button id="merge-run" onclick="runMerge()" style="padding:0.35rem 0.85rem;background:#238636;border:none;border-radius:4px;color:#fff;cursor:pointer;font-size:0.85rem">Merge</button>
      <button onclick="closeMerge()" style="padding:0.35rem 0.85rem;background:#21262d;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;cursor:pointer;font-size:0.85rem">Cancel</button>
    </div>
    <input id="merge-guidance" type="text" placeholder="Optional: merge guidance (e.g. focus on technical aspects)"
      style="padding:0.5rem 1rem;background:#0d1117;border:none;border-bottom:1px solid #30363d;color:#c9d1d9;font-size:0.85rem">
    <textarea id="merge-input" placeholder="Paste the second article or content here..." style="flex:1;background:#0d1117;border:none;color:#c9d1d9;padding:1rem;font-family:inherit;font-size:0.9rem;resize:none;outline:none"></textarea>
  </div>
  <div id="chat-pane">
    <div id="chat-header">Chat <button onclick="document.getElementById('chat-messages').innerHTML='';clearChatHistory();" style="float:right;background:none;border:none;color:#8b949e;cursor:pointer;font-size:0.75rem;padding:0 0.3rem" title="Clear chat">Clear</button></div>
    <div id="chat-messages"></div>
    <div id="chat-input-area">
      <textarea id="chat-input" rows="1" placeholder="Discuss with Chat, or click Edit to apply changes. Enter = Edit, Alt+Enter = Chat, Shift+Enter = newline." onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();sendChat(event.altKey?'chat':'edit');}"></textarea>
      <button id="chat-talk" onclick="sendChat('chat')" title="Discuss without changing the article">Chat</button>
      <button id="chat-send" onclick="sendChat('edit')" title="Propose a rewrite to apply to the article">Edit</button>
    </div>
  </div>
</div>
<div id="overlay" onclick="hideArticles()"></div>
<div id="articles-panel"><h3>Saved Articles</h3><div id="articles-list"></div></div>
`

// techwriterJS is the app's main JS, appended after webui.BaseJS().
const techwriterJS = `
var currentId = null;

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

function showMergeDialog() {
  var body = document.getElementById('editor').value.trim();
  if (!body) { alert('Current article is empty. Load or write something first.'); return; }
  document.getElementById('merge-pane').style.display = 'flex';
  document.getElementById('merge-input').focus();
}

function closeMerge() {
  document.getElementById('merge-pane').style.display = 'none';
  document.getElementById('merge-input').value = '';
  document.getElementById('merge-guidance').value = '';
}

function runMerge() {
  var subject = document.getElementById('subject').value.trim();
  var body = document.getElementById('editor').value.trim();
  var other = document.getElementById('merge-input').value.trim();
  var guidance = document.getElementById('merge-guidance').value.trim();
  if (!other) { alert('Paste content to merge with.'); return; }

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
      }
    });
    addChatMsg('assistant', mergeHtml, data.content, mergeTitle);
    closeMerge();
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
  });
}

function newArticle() {
  if (personaEditMode) return; // stay in persona mode — use Cancel to exit
  currentId = null;
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

function showArticles() {
  document.getElementById('overlay').style.display = 'block';
  document.getElementById('articles-panel').style.display = 'block';
  var list = document.getElementById('articles-list');
  list.innerHTML = '<div style="color:#8b949e;text-align:center;padding:0.5rem">Loading...</div>';

  fetch('/api/list').then(function(r) { return r.json(); }).then(function(items) {
    if (!items || items.length === 0) {
      list.innerHTML = '<div style="color:#8b949e;text-align:center;padding:0.5rem">No saved articles.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      var date = '';
      try { date = new Date(item.Date).toLocaleDateString(); } catch(e) {}
      html += '<div class="article-item" onclick="loadArticle(\'' + item.ID + '\')">';
      html += '<div><div class="title">' + escapeHtml(item.Subject || 'Untitled') + '</div><div class="date">' + date + '</div></div>';
      html += '<button class="del-btn" onclick="event.stopPropagation();deleteArticle(\'' + item.ID + '\',this.closest(\'.article-item\'))">&times;</button>';
      html += '</div>';
    }
    list.innerHTML = html;
  });
}

function hideArticles() {
  document.getElementById('overlay').style.display = 'none';
  document.getElementById('articles-panel').style.display = 'none';
}

function loadArticle(id) {
  hideArticles();
  return fetch('/api/load/' + id).then(function(r) { return r.json(); }).then(function(rec) {
    currentId = rec.ID;
    document.getElementById('subject').value = rec.Subject || '';
    document.getElementById('editor').value = rec.Body || '';
    // Loading a different article invalidates the prior discussion —
    // keep conversation scoped to a single article so Edit can't act
    // on notes from a different file.
    document.getElementById('chat-messages').innerHTML = '';
    clearChatHistory();
  });
}

function deleteArticle(id, el) {
  fetch('/api/delete/' + id, {method: 'DELETE'}).then(function() {
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
`
