package codewriter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	snippetTable        = "codewriter_snippets"
	valueTable          = "codewriter_values"
	contextTable        = "codewriter_contexts"
	cwRevisionTable     = "codewriter_revisions"
	maxSnippetRevisions = 50
)

// SnippetRevision is a point-in-time snapshot created on each snippet save.
type SnippetRevision struct {
	ID        string `json:"id"`
	SnippetID string `json:"snippet_id"`
	Name      string `json:"name"`
	Lang      string `json:"lang"`
	Code      string `json:"code"`
	Date      string `json:"date"`
}

// SnippetRecord stores a saved code snippet with optional variables.
type SnippetRecord struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Lang string            `json:"lang"`
	Code string            `json:"code"`
	Vars map[string]string `json:"vars"` // variable_name -> substitution text
	Date string            `json:"date"`
}

// ContextRecord stores a saved context block (reference schemas/docs/notes).
type ContextRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Body string `json:"body"`
	Date string `json:"date"`
}

// SavedValue is a named snippet of text the user can paste into scripts.
// Could be a hostname, a shell command like $(python3 get_pass.py), an
// env var like $DB_HOST, a connection string, etc.
type SavedValue struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`  // optional description
	Value string `json:"value"` // the text to paste
	Date  string `json:"date"`
}

func (T *CodeWriterAgent) WebPath() string { return "/codewriter" }
func (T *CodeWriterAgent) WebName() string { return "CodeWriter" }
func (T *CodeWriterAgent) WebDesc() string {
	return "Generate shell scripts, SQL queries, and code snippets."
}

func (T *CodeWriterAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	// Framework page (page.go + CodeWriterPanel) is the only UI — it
	// covers snippets, chat, diff, revisions, values, contexts, and
	// collections. The old hand-rolled /codewriter/legacy surface has
	// been retired.
	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", T.handleCodeWriterPage)
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/snippets", T.handleSnippets)
	sub.HandleFunc("/api/snippet/", T.handleSnippet)
	sub.HandleFunc("/api/values", T.handleValues)
	sub.HandleFunc("/api/value/", T.handleValue)
	sub.HandleFunc("/api/contexts", T.handleContexts)
	sub.HandleFunc("/api/context/", T.handleContext)
	sub.HandleFunc("/api/collections", T.handleCollectionsList)
	sub.HandleFunc("/api/reference-sources", T.handleReferenceSources)
	sub.HandleFunc("/api/revisions/", T.handleRevisions)
	sub.HandleFunc("/api/revision/", T.handleRevision)
	sub.HandleFunc("/api/suggest-name", T.handleSuggestName)
	MountSubMux(mux, prefix, sub)
	RegisterUserDataHandler(&codeWriterUserData{agent: T})

	cwDB := T.DB
	SaveSnippetFunc = func(userID, name, lang, code string) (string, error) {
		udb := UserDB(cwDB, userID)
		id := UUIDv4()
		rec := SnippetRecord{
			ID:   id,
			Name: name,
			Lang: lang,
			Code: code,
			Date: time.Now().Format(time.RFC3339),
		}
		udb.Set(snippetTable, rec.ID, rec)
		revID := UUIDv4()
		udb.Set(cwRevisionTable, revID, SnippetRevision{
			ID:        revID,
			SnippetID: rec.ID,
			Name:      rec.Name,
			Lang:      rec.Lang,
			Code:      rec.Code,
			Date:      rec.Date,
		})
		return id, nil
	}
}

// handleChat receives the current editor code + a chat message, sends to the
// worker LLM, and returns the response. If the response contains code, we
// flag it so the frontend can show an "Apply" button.
func (T *CodeWriterAgent) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Lang    string `json:"lang"`
		Code    string `json:"code"`
		Context string `json:"context"`
		Message string `json:"message"`
		// Mode controls whether the LLM is allowed to propose code
		// changes. "edit" (default) is the legacy behavior — the LLM
		// can emit a fenced code block which the UI offers to apply
		// to the editor. "chat" tells the LLM to discuss and explain
		// only, never emit code — used for "talk me through this"
		// / "what would you do" conversations before committing to
		// a change. Absent / blank is treated as "edit" for back-compat
		// with older clients.
		Mode string `json:"mode"`
		// Collections is the set of Document Collection IDs the user
		// picked as reference corpora for this turn. Top chunks are
		// RAG-retrieved (best-effort) from each and injected as grounding
		// alongside the manual Context block. Empty = no collection RAG.
		Collections []string `json:"collections"`
		// References are reference-source selections the user picked in
		// the chat header ([{kind, item_id}]) — knowledge another gohort
		// service gathered (servitor systems, MCP doc sources). Each
		// source's cached text is injected into the system prompt, and
		// its per-item tools (search / facts / live investigate) ride the
		// chat so the LLM can dig deeper when the cached picture doesn't
		// answer.
		References []ReferenceSelection `json:"references"`
		// History is the prior conversation, client-maintained.
		// Allows Chat → Edit to carry discussion context so Edit can
		// act on what was just discussed. File state (code/context)
		// attaches to the CURRENT message only — never embedded into
		// past turns — so the LLM always sees the file as it is now
		// rather than stale snapshots from earlier turns.
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
	is_regex := req.Lang == "regex"

	var code_context string
	if req.Context != "" {
		label := "Reference context (table schemas, docs, notes)"
		if is_regex {
			label = "Target description (what the pattern should match)"
		}
		code_context = fmt.Sprintf("\n\n%s:\n```\n%s\n```", label, req.Context)
	}
	if req.Code != "" {
		lang := req.Lang
		if lang == "" {
			lang = "code"
		}
		if is_regex {
			code_context += fmt.Sprintf("\n\nTest samples (one per line; lines prefixed `+` should match, `-` should not match, unprefixed are ambiguous):\n```\n%s\n```", req.Code)
		} else {
			code_context += fmt.Sprintf("\n\nCurrent script (%s):\n```%s\n%s\n```", req.Name, lang, req.Code)
		}
	} else if req.Name != "" && !is_regex {
		code_context += fmt.Sprintf("\n\nScript name: %s\n(Editor is empty -- this is a new script.)", req.Name)
	}

	// Reference Collections — best-effort RAG over the user's picked
	// Document Collections via the shared core primitive. Degrades
	// silently on any miss (no auth, no embedding backend, empty corpus)
	// so chat always works; retrieved chunks ride on the user message as
	// a labeled grounding block, same shape as the manual Context.
	if len(req.Collections) > 0 {
		if uid := AuthCurrentUser(r); uid != "" {
			// Collections live in the shared collections home, NOT
			// codewriter's own bucket — reach them via CollectionsDB().
			hits := SearchCollections(r.Context(), CollectionsDB(), uid, req.Collections, req.Message, 6)
			code_context += formatCollectionRefs(hits)
		}
	}

	prompt := req.Message + code_context

	// Build system prompt with selected language hint and available value sets.
	system_prompt := T.SystemPrompt()
	if is_regex {
		system_prompt += "\n\nThe user is building a regular expression. The editor holds test samples (one per line; `+` prefix = should match, `-` prefix = should not match). The context holds a plain-English description of what the pattern should match. Return a single regex in a fenced ```regex block, then a short explanation naming each capture group and why each sample matches or doesn't. Default to PCRE-compatible syntax unless the user specifies a flavor (ERE, Go re2, JavaScript, etc.). Do not wrap the regex in delimiters like /.../ unless the user asked for a specific language's literal syntax."
	} else if req.Lang != "" {
		system_prompt += fmt.Sprintf("\n\nThe user is currently working in %s. Default to %s for code output unless they specify otherwise.", req.Lang, req.Lang)
	}
	system_prompt += T.buildValuePrompt()

	if chatOnly {
		// Discussion-only mode. The user wants to talk through an
		// approach before touching the editor, so the LLM must not
		// emit a fenced code block (the UI would offer to apply it,
		// defeating the whole point of the mode). Explanation via
		// prose, short inline snippets as backticks if unavoidable.
		system_prompt += "\n\nDISCUSSION MODE: the user is chatting about the code, not asking for it to be changed. Do NOT write out a revised full script or propose an applyable change. Do NOT emit a fenced code block (```). Explain your thinking, ask clarifying questions, or describe the approach you'd take. Short inline snippets using single backticks are fine. If the user asks for the actual edit, tell them to click Edit instead of Chat."
	}

	// Reference sources — knowledge gathered by other gohort services
	// (servitor systems, MCP doc sources) that the user attached in the
	// chat header. Cached material is injected into the system prompt
	// (closest to the user message), and each attached item's own tools
	// ride the chat so the LLM can search deeper or run a live
	// investigation when the cached picture doesn't answer.
	var ref_tools []AgentToolDef
	if len(req.References) > 0 {
		if uid := AuthCurrentUser(r); uid != "" {
			if ref := FetchReferences(r.Context(), uid, req.Message, req.References); ref != "" {
				system_prompt += "\n\n" + ref
			}
			for _, sel := range req.References {
				ref_tools = append(ref_tools, ReferenceItemTools(uid, sel.Kind, sel.ItemID)...)
			}
			if len(ref_tools) > 0 {
				system_prompt += "\n\nThe attached reference source also provides tools. Use them when the reference context above doesn't answer the question, or when the user asks about the CURRENT state of the system — the investigate tool runs a live session when cached knowledge isn't enough."
			}
		}
	}

	agent := &AppCore{LLM: T.AppCore.LLM}

	// Build messages: prior turns (capped) + current message with file
	// context. File state rides on the last user message only so the
	// LLM sees the current editor, not stale copies.
	const maxHistoryTurns = 20
	hist := req.History
	if len(hist) > maxHistoryTurns {
		hist = hist[len(hist)-maxHistoryTurns:]
	}
	messages := make([]Message, 0, len(hist)+1)
	for _, h := range hist {
		role := h.Role
		if role != "user" && role != "assistant" {
			continue // drop malformed turns
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		messages = append(messages, Message{Role: role, Content: h.Content})
	}
	messages = append(messages, Message{Role: "user", Content: prompt})

	resp, err := agent.LeadChatWithTools(r.Context(), messages, ref_tools,
		WithSystemPrompt(system_prompt), WithRouteKey("app.codewriter"))

	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))

	// Detect if the response contains a fenced code block.
	code_content := ""
	if strings.HasPrefix(text, "CODE:") {
		lines := strings.SplitN(text, "\n", 2)
		if len(lines) > 1 {
			text = strings.TrimSpace(lines[1])
		} else {
			text = strings.TrimPrefix(text, "CODE:")
		}
	}

	// Extract the first fenced code block as the applyable code.
	if idx := strings.Index(text, "```"); idx >= 0 {
		after := text[idx+3:]
		// Skip the language tag on the opening fence.
		if nl := strings.Index(after, "\n"); nl >= 0 {
			after = after[nl+1:]
		}
		if end := strings.Index(after, "```"); end >= 0 {
			code_content = after[:end]
			if len(code_content) > 0 && code_content[len(code_content)-1] == '\n' {
				code_content = code_content[:len(code_content)-1]
			}
		}
	}

	result := map[string]any{"content": text}
	// In chat-only mode, never offer an apply — even if the LLM ignored
	// the system-prompt rule and emitted a fenced block anyway. The
	// client also guards for this, but double-guarding here keeps the
	// mode clean at the API level.
	if !chatOnly && code_content != "" {
		result["code"] = code_content
		result["type"] = "code"
	} else {
		result["type"] = "chat"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// formatCollectionRefs renders retrieved collection chunks as a labeled
// reference block appended to the prompt. Returns "" for no hits so the
// caller can append unconditionally.
func formatCollectionRefs(hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nReference material from your attached collections (grounding — use what's relevant, ignore the rest):\n")
	for _, h := range hits {
		label := strings.TrimSpace(h.Section)
		if label == "" {
			label = h.Source
		}
		b.WriteString("\n--- ")
		b.WriteString(label)
		b.WriteString(" ---\n")
		b.WriteString(strings.TrimSpace(h.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// handleCollectionsList returns the collections visible to the current
// user (their own + deployment-scoped), as {id, name} for the picker.
// Server-side via the core ListCollections primitive — codewriter never
// reaches into orchestrate's HTTP surface for this.
func (T *CodeWriterAgent) handleCollectionsList(w http.ResponseWriter, r *http.Request) {
	uid := AuthCurrentUser(r)
	if uid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	out := []item{}
	// Collections live in the shared collections home, not codewriter's
	// own bucket — list from UserDB(CollectionsDB(), uid). Description
	// rides along so the picker can show what each store holds; doc/chunk
	// counts are intentionally omitted (they'd cost an EmbeddedChunks walk
	// per collection — the picker renders the size line only when present).
	for _, c := range ListCollections(UserDB(CollectionsDB(), uid), uid) {
		out = append(out, item{ID: c.ID, Name: c.Name, Description: c.Description})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleReferenceSources feeds the chat-header reference picker: every
// registered reference source's items available to this user, grouped. The
// data is generic (core.ReferenceGroup) — codewriter doesn't know or care
// which services contributed (servitor systems, MCP doc sources, …).
func (T *CodeWriterAgent) handleReferenceSources(w http.ResponseWriter, r *http.Request) {
	uid := AuthCurrentUser(r)
	if uid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	groups := ReferenceGroups(uid)
	if groups == nil {
		groups = []ReferenceGroup{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(groups)
}

// buildValuePrompt appends a note about placeholders to the system prompt.
func (T *CodeWriterAgent) buildValuePrompt() string {
	return "\n\nWhen generating code, use {{PLACEHOLDER}} syntax for values the user would likely need to fill in, such as credentials, hostnames, paths, and environment-specific settings. Examples: {{PASSWORD}}, {{DB_HOST}}, {{OUTPUT_PATH}}. The user has a UI that detects these placeholders and lets them fill in values."
}

// handleSnippets handles list (GET) and create/update (POST).
func (T *CodeWriterAgent) handleSnippets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		T.handleListSnippets(w, r)
	case http.MethodPost:
		T.handleSaveSnippet(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleListSnippets(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	var items []SnippetRecord
	for _, key := range udb.Keys(snippetTable) {
		var rec SnippetRecord
		if udb.Get(snippetTable, key, &rec) {
			items = append(items, rec)
		}
	}
	if items == nil {
		items = []SnippetRecord{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (T *CodeWriterAgent) handleSaveSnippet(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var req SnippetRecord
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Code == "" {
		http.Error(w, "name and code required", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	req.Date = time.Now().Format(time.RFC3339)
	udb.Set(snippetTable, req.ID, req)
	revID := UUIDv4()
	udb.Set(cwRevisionTable, revID, SnippetRevision{
		ID:        revID,
		SnippetID: req.ID,
		Name:      req.Name,
		Lang:      req.Lang,
		Code:      req.Code,
		Date:      req.Date,
	})
	type keyDate struct{ key, date string }
	var all []keyDate
	for _, k := range udb.Keys(cwRevisionTable) {
		var rev SnippetRevision
		if udb.Get(cwRevisionTable, k, &rev) && rev.SnippetID == req.ID {
			all = append(all, keyDate{k, rev.Date})
		}
	}
	if len(all) > maxSnippetRevisions {
		sort.Slice(all, func(i, j int) bool { return all[i].date < all[j].date })
		for _, kd := range all[:len(all)-maxSnippetRevisions] {
			udb.Unset(cwRevisionTable, kd.key)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "rev_id": revID, "name": req.Name, "lang": req.Lang})
}

// handleSnippet handles GET (load) and DELETE for /api/snippet/{id}.
func (T *CodeWriterAgent) handleSnippet(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/snippet/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec SnippetRecord
		if !udb.Get(snippetTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		udb.Unset(snippetTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- HTML / CSS / JS ---

// --- Saved Values ---

func (T *CodeWriterAgent) handleValues(w http.ResponseWriter, r *http.Request) {
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
		var items []SavedValue
		for _, key := range udb.Keys(valueTable) {
			var rec SavedValue
			if udb.Get(valueTable, key, &rec) {
				items = append(items, rec)
			}
		}
		if items == nil {
			items = []SavedValue{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req SavedValue
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
		udb.Set(valueTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleValue(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/value/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec SavedValue
		if !udb.Get(valueTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		udb.Unset(valueTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Saved Contexts ---

func (T *CodeWriterAgent) handleContexts(w http.ResponseWriter, r *http.Request) {
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
		var items []ContextRecord
		for _, key := range udb.Keys(contextTable) {
			var rec ContextRecord
			if udb.Get(contextTable, key, &rec) {
				items = append(items, rec)
			}
		}
		if items == nil {
			items = []ContextRecord{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)

	case http.MethodPost:
		if udb == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		var req ContextRecord
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
		udb.Set(contextTable, req.ID, req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(req)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleContext(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/context/")
	if id == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var rec ContextRecord
		if !udb.Get(contextTable, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rec)
	case http.MethodDelete:
		udb.Unset(contextTable, id)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CodeWriterAgent) handleRevisions(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	snippetID := strings.TrimPrefix(r.URL.Path, "/api/revisions/")
	if snippetID == "" || udb == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	type RevisionSummary struct {
		ID   string `json:"id"`
		Date string `json:"date"`
	}
	var summaries []RevisionSummary
	for _, k := range udb.Keys(cwRevisionTable) {
		var rev SnippetRevision
		if udb.Get(cwRevisionTable, k, &rev) && rev.SnippetID == snippetID {
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

func (T *CodeWriterAgent) handleRevision(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	revID := strings.TrimPrefix(r.URL.Path, "/api/revision/")
	if revID == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var rev SnippetRevision
	if !udb.Get(cwRevisionTable, revID, &rev) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rev)
}

func (T *CodeWriterAgent) handleSuggestName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Lang string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}
	preview := req.Code
	if len(preview) > 1000 {
		preview = preview[:1000]
	}
	lang := req.Lang
	if lang == "" {
		lang = "code"
	}
	agent := &AppCore{LLM: T.AppCore.LLM}
	session := agent.CreateSession(WORKER)
	resp, err := session.Chat(r.Context(), []Message{
		{Role: "user", Content: fmt.Sprintf("Suggest a concise snake_case filename (max 5 words, no extension) for this %s script:\n\n```\n%s\n```", lang, preview)},
	}, WithSystemPrompt("Reply with ONLY the name in snake_case. No extension. No quotes."), WithMaxTokens(16), WithThink(false))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := strings.TrimSpace(ResponseText(resp))
	name = strings.Trim(name, "`\"'")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": name})
}
