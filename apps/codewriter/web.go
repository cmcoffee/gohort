package codewriter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

const (
	snippetTable        = "codewriter_snippets"
	valueTable          = "codewriter_values"
	contextTable        = "codewriter_contexts"
	templateTable       = "codewriter_templates"
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

// TemplateRecord is a user-saved document skeleton. Same shape as the
// host-declared ui.DocTemplate so the picker can render both kinds in
// one list; the Owner distinction is that these carry an ID and can be
// deleted, and built-ins cannot.
type TemplateRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"description"`
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
	// Framework page (page.go + CodeEditorPanel) is the only UI — it
	// covers snippets, chat, diff, revisions, values, contexts, and
	// collections. The old hand-rolled /codewriter/legacy surface has
	// been retired.
	sub := NewWebUI(T, prefix, AppUIAssets{})
	// Writers travel in the bundle format (writer_artifact.go); here because
	// T.DB is live.
	RegisterArtifactType(&writerArtifact{app: T})
	sub.HandleFunc("/", T.handleCodeWriterPage)
	sub.HandleFunc("/api/chat", T.handleChat)
	sub.HandleFunc("/api/snippets", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, listRecords[SnippetRecord](snippetTable)),
		"POST": jsonapi.Post(T.DB, T.handleSaveSnippet),
	}.Serve)
	sub.HandleFunc("/api/snippet/", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, recordAt[SnippetRecord]("/api/snippet/", snippetTable)),
		"DELETE": jsonapi.Delete(T.DB, deleteRecordAt("/api/snippet/", snippetTable)),
	}.Serve)
	sub.HandleFunc("/api/values", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, listRecords[SavedValue](valueTable)),
		"POST": jsonapi.Post(T.DB, T.handleValueSave),
	}.Serve)
	sub.HandleFunc("/api/value/", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, recordAt[SavedValue]("/api/value/", valueTable)),
		"DELETE": jsonapi.Delete(T.DB, deleteRecordAt("/api/value/", valueTable)),
	}.Serve)
	sub.HandleFunc("/api/contexts", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, listRecords[ContextRecord](contextTable)),
		"POST": jsonapi.Post(T.DB, T.handleContextSave),
	}.Serve)
	sub.HandleFunc("/api/context/", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, recordAt[ContextRecord]("/api/context/", contextTable)),
		"DELETE": jsonapi.Delete(T.DB, deleteRecordAt("/api/context/", contextTable)),
	}.Serve)
	sub.HandleFunc("/api/writers", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, listRecords[WriterRecord](writerTable)),
		"POST": jsonapi.Post(T.DB, T.handleWriterSave),
	}.Serve)
	sub.HandleFunc("/api/writer/", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.handleWriter),
		"DELETE": jsonapi.Delete(T.DB, T.handleWriterDelete),
	}.Serve)
	sub.HandleFunc("/api/assist", T.handleAssist)
	sub.HandleFunc("/api/rules", func(w http.ResponseWriter, r *http.Request) {
		HandleDocRules(w, r, T.DB, "codewriter")
	})
	sub.HandleFunc("/api/templates", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleTemplates),
		"POST": jsonapi.Post(T.DB, T.handleTemplateSave),
	}.Serve)
	sub.HandleFunc("/api/template/", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, recordAt[TemplateRecord]("/api/template/", templateTable)),
		"DELETE": jsonapi.Delete(T.DB, deleteRecordAt("/api/template/", templateTable)),
	}.Serve)
	sub.HandleFunc("/api/collections", jsonapi.Get(T.DB, T.handleCollectionsList))
	sub.HandleFunc("/api/reference-sources", jsonapi.Get(T.DB, T.handleReferenceSources))
	sub.HandleFunc("/api/revisions/", jsonapi.Get(T.DB, T.handleRevisions))
	sub.HandleFunc("/api/revision/", jsonapi.Get(T.DB, recordAt[SnippetRevision]("/api/revision/", cwRevisionTable)))
	sub.HandleFunc("/api/suggest-name", jsonapi.Post(T.DB, T.handleSuggestName))
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
		// Writer names the configured WRITER this turn runs under (writers.go):
		// a named profile whose knowledge foundation governs everything written
		// here. Distinct from the three fields above, which are per-turn
		// attachments the user ticks — this one is chosen from the panel's
		// picker and persists across turns and sessions. Empty = no writer.
		// Wire name is the panel's generic `profile`; the app's own name for the
		// concept is Writer. core/ui names no host's nouns (see CollectionsNoun),
		// so the contract stays generic and the domain word lives here.
		Writer string `json:"profile"`
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
		code_context = fmt.Sprintf("\n\n%s:\n%s\n%s\n%s", label, DocFence(req.Context), req.Context, DocFence(req.Context))
	}
	if req.Code != "" {
		lang := req.Lang
		if lang == "" {
			lang = "code"
		}
		if is_regex {
			code_context += fmt.Sprintf("\n\nTest samples (one per line; lines prefixed `+` should match, `-` should not match, unprefixed are ambiguous):\n%s\n%s\n%s", DocFence(req.Code), req.Code, DocFence(req.Code))
		} else {
			f := DocFence(req.Code)
			code_context += fmt.Sprintf("\n\nCurrent script (%s):\n%s%s\n%s\n%s", req.Name, f, lang, req.Code, f)
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
	var ref_context string
	// The active WRITER's standing sources join whatever the user attached for
	// this one turn. Merged here rather than handled separately because they
	// are the same kind of thing and want the same treatment — the only
	// difference is that one selection was made once and the other per
	// question. A writer's material must not take a different, weaker path
	// than a checkbox ticked ten seconds ago.
	refs := req.References
	var writer WriterRecord
	if uid := AuthCurrentUser(r); uid != "" && strings.TrimSpace(req.Writer) != "" {
		if wr, found := loadWriter(UserDB(T.DB, uid), req.Writer); found {
			writer = wr
			// Writer's sources FIRST, then the per-turn picks: a duplicate
			// resolves to the same item either way, and ordering the standing
			// material first keeps the prompt prefix stable across turns where
			// only the ad-hoc pick changes.
			refs = append(append(ReferenceSelections{}, wr.Sources...), refs...)
		}
	}
	// The same item picked in both places is one reference, not two: fetched
	// once, and its tools built once. Twice put every tool name in the request
	// twice, which some providers refuse outright.
	refs = uniqueReferences(refs)
	if len(refs) > 0 {
		if uid := AuthCurrentUser(r); uid != "" {
			// Rides the current user message, not the system prompt — the
			// retrieved text changes per query, and a per-turn system prompt
			// re-prefills the whole thread on the worker every turn.
			if ref := FetchReferences(r.Context(), uid, req.Message, refs); ref != "" {
				ref_context = "\n\n" + ref
			}
			for _, sel := range refs {
				ref_tools = append(ref_tools, ReferenceItemTools(uid, sel.Kind, sel.ItemID)...)
			}
			if len(ref_tools) > 0 {
				system_prompt += "\n\nThe attached reference source also provides tools. Use them when the reference context above doesn't answer the question, or when the user asks about the CURRENT state of the system: the investigate tool runs a live session when cached knowledge isn't enough."
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
	messages = append(messages, Message{Role: "user", Content: prompt + ref_context})

	// The writer's standing note — what its material IS and how far it binds.
	// AFTER the mode rules on purpose: those are the app's output CONTRACT
	// (discussion mode emits no fenced block, edit mode returns one applyable
	// block) and the Apply button reads that shape, so a note about domain
	// knowledge must not be the last word on it. The sources govern WHAT the
	// code says; the mode rules govern what the reply looks like.
	system_prompt += writer.sourceBlock()

	// Standing rules append to the system prompt on every chat turn, the
	// same as on assist — they're constraints on output, not on mode.
	if uid := AuthCurrentUser(r); uid != "" {
		system_prompt += DocRulesSection(UserDB(T.DB, uid), "codewriter")
	}

	resp, err := agent.LeadChatWithTools(r.Context(), messages, ref_tools,
		WithSystemPrompt(system_prompt), WithRouteKey("app.codewriter"))

	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	text := strings.TrimSpace(ResponseText(resp))

	// Detect if the response contains a fenced code block.
	// (The old CODE: sentinel strip is gone — no prompt ever requested it.)
	code_content := ""

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
	// The shared hit shape (core.HitFormat): title, section, page locator
	// and provenance kind on every passage. Whole chunks, no doc ids — the
	// chat has no fetch tool to pass one to.
	return "\n\nReference material from your attached collections (grounding, use what's relevant, ignore the rest):\n\n" +
		HitFormat{}.Render(hits) + "\n"
}

// handleCollectionsList returns the collections visible to the current
// user (their own + deployment-scoped), as {id, name} for the picker.
// Server-side via the core ListCollections primitive — codewriter never
// reaches into orchestrate's HTTP surface for this.
func (T *CodeWriterAgent) handleCollectionsList(c *jsonapi.Call) (any, error) {
	uid := c.User
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
	return out, nil
}

// handleReferenceSources feeds the chat-header reference picker: every
// registered reference source's items available to this user, grouped. The
// data is generic (core.ReferenceGroup) — codewriter doesn't know or care
// which services contributed (servitor systems, MCP doc sources, …).
func (T *CodeWriterAgent) handleReferenceSources(c *jsonapi.Call) (any, error) {
	groups := ReferenceGroups(c.User)
	if groups == nil {
		groups = []ReferenceGroup{}
	}
	return groups, nil
}

// buildValuePrompt appends a note about placeholders to the system prompt.
func (T *CodeWriterAgent) buildValuePrompt() string {
	return "\n\nWhen generating code, use {{PLACEHOLDER}} syntax for values the user would likely need to fill in, such as credentials, hostnames, paths, and environment-specific settings. Examples: {{PASSWORD}}, {{DB_HOST}}, {{OUTPUT_PATH}}. The user has a UI that detects these placeholders and lets them fill in values."
}

// handleSaveSnippet creates or updates a snippet and keeps a revision of it,
// trimming the oldest past maxSnippetRevisions.
func (T *CodeWriterAgent) handleSaveSnippet(c *jsonapi.Call, req SnippetRecord) (any, error) {
	udb := c.UDB
	if udb == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	if req.Name == "" || req.Code == "" {
		return nil, jsonapi.BadRequest("name and code required")
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
	return map[string]interface{}{"id": req.ID, "rev_id": revID, "name": req.Name, "lang": req.Lang}, nil
}

// The panel's saved records (snippets, values, contexts, templates, writers)
// all live one table each in the caller's own store, so listing, fetching and
// deleting one is the same three steps for every kind. A record is reached
// only through its owner's store: another user's id is simply not there.

// records is every R in table in udb.
func records[R any](udb Database, table string) []R {
	items := []R{}
	if udb == nil {
		return items
	}
	for _, key := range udb.Keys(table) {
		var rec R
		if udb.Get(table, key, &rec) {
			items = append(items, rec)
		}
	}
	return items
}

// listRecords answers with every R in the caller's table.
func listRecords[R any](table string) func(*jsonapi.Call) (any, error) {
	return func(c *jsonapi.Call) (any, error) { return records[R](c.UDB, table), nil }
}

// recordAt answers with the R in the caller's table whose id the path names
// after prefix.
func recordAt[R any](prefix, table string) func(*jsonapi.Call) (any, error) {
	return func(c *jsonapi.Call) (any, error) {
		id := strings.TrimPrefix(c.R.URL.Path, prefix)
		var rec R
		if id == "" || c.UDB == nil || !c.UDB.Get(table, id, &rec) {
			return nil, jsonapi.NotFound("not found")
		}
		return rec, nil
	}
}

// deleteRecordAt removes the record in the caller's table whose id the path
// names after prefix.
func deleteRecordAt(prefix, table string) func(*jsonapi.Call, struct{}) (any, error) {
	return func(c *jsonapi.Call, _ struct{}) (any, error) {
		id := strings.TrimPrefix(c.R.URL.Path, prefix)
		if id == "" || c.UDB == nil {
			return nil, jsonapi.NotFound("not found")
		}
		c.UDB.Unset(table, id)
		return map[string]any{"deleted": id}, nil
	}
}

// --- HTML / CSS / JS ---

// --- Saved Values ---

// handleValueSave creates or updates a saved value.
func (T *CodeWriterAgent) handleValueSave(c *jsonapi.Call, req SavedValue) (any, error) {
	if c.UDB == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	if req.Name == "" {
		return nil, jsonapi.BadRequest("name required")
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	req.Date = time.Now().Format(time.RFC3339)
	c.UDB.Set(valueTable, req.ID, req)
	return req, nil
}

// --- Saved Contexts ---

// handleContextSave creates or updates a context.
func (T *CodeWriterAgent) handleContextSave(c *jsonapi.Call, req ContextRecord) (any, error) {
	if c.UDB == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	if req.Name == "" {
		return nil, jsonapi.BadRequest("name required")
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	req.Date = time.Now().Format(time.RFC3339)
	c.UDB.Set(contextTable, req.ID, req)
	return req, nil
}

func (T *CodeWriterAgent) handleRevisions(c *jsonapi.Call) (any, error) {
	type RevisionSummary struct {
		ID   string `json:"id"`
		Date string `json:"date"`
	}
	summaries := []RevisionSummary{}
	snippetID := strings.TrimPrefix(c.R.URL.Path, "/api/revisions/")
	if snippetID == "" || c.UDB == nil {
		return summaries, nil
	}
	for _, rev := range records[SnippetRevision](c.UDB, cwRevisionTable) {
		if rev.SnippetID == snippetID {
			summaries = append(summaries, RevisionSummary{ID: rev.ID, Date: rev.Date})
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Date < summaries[j].Date })
	return summaries, nil
}

func (T *CodeWriterAgent) handleSuggestName(c *jsonapi.Call, req suggestNameReq) (any, error) {
	if req.Code == "" {
		return nil, jsonapi.BadRequest("code required")
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
	resp, err := session.Chat(c.R.Context(), []Message{
		{Role: "user", Content: fmt.Sprintf("Suggest a concise snake_case filename (max 5 words, no extension) for this %s script:\n\n%s\n%s\n%s", lang, DocFence(preview), preview, DocFence(preview))},
	}, WithSystemPrompt("Reply with ONLY the name in snake_case. No extension. No quotes."), WithMaxTokens(304), WorkerJudgeThink())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(ResponseText(resp))
	name = strings.Trim(name, "`\"'")
	return map[string]string{"name": name}, nil
}

// suggestNameReq is what handleSuggestName is posted.
type suggestNameReq struct {
	Code string `json:"code"`
	Lang string `json:"lang"`
}

// handleTemplates lists user-saved markdown templates, by name. Same per-user
// store and shape as contexts.
func (T *CodeWriterAgent) handleTemplates(c *jsonapi.Call) (any, error) {
	items := records[TemplateRecord](c.UDB, templateTable)
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items, nil
}

// handleTemplateSave creates or updates a template.
func (T *CodeWriterAgent) handleTemplateSave(c *jsonapi.Call, req TemplateRecord) (any, error) {
	udb := c.UDB
	if udb == nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "no database")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, jsonapi.BadRequest("name required")
	}
	if strings.TrimSpace(req.Body) == "" {
		return nil, jsonapi.BadRequest("body required")
	}
	// Saving under a name that already exists REPLACES it. Without
	// this, "save as template" after an edit silently accumulates
	// near-duplicate rows and the picker becomes a junk drawer.
	if req.ID == "" {
		req.ID = resolveTemplateID(records[TemplateRecord](udb, templateTable), req.Name)
	}
	if req.ID == "" {
		req.ID = UUIDv4()
	}
	req.Date = time.Now().Format(time.RFC3339)
	udb.Set(templateTable, req.ID, req)
	return req, nil
}

// handleAssist powers the draft-with-me workbench over a markdown
// document: the text on one side, a conversation on the other. Same
// wire contract as the agent editor's suggest endpoint — in
// {section, message, draft, history}, out {reply, value} — so the shared
// core/ui workbench drives both without knowing which app it's talking
// to.
//
// Deliberately separate from handleChat. That endpoint is about CODE:
// it frames the body as a script, offers regex handling, and returns a
// proposal the diff pane reviews. This one is about a DOCUMENT and a
// section of it, and returns a whole replacement the version walk
// handles. Folding them together would mean one prompt trying to be
// both, which is how both get worse.
func (T *CodeWriterAgent) handleAssist(w http.ResponseWriter, r *http.Request) {
	assist.Handler(assist.Spec{
		Route: "app.codewriter.assist", Label: "CodeWriter: document assist", App: "/codewriter",
		Core: &T.AppCore, Think: assist.Off,
		Allow: func(w http.ResponseWriter, r *http.Request) bool {
			_, _, ok := RequireUser(w, r, T.DB)
			return ok
		},
		Prompt: func(r *http.Request, req assist.Request) (string, error) {
			// The user's standing rules ride on every assist turn, the same
			// way they do on chat: a draft that ignores them gets rejected on
			// sight.
			var rules string
			if uid := AuthCurrentUser(r); uid != "" {
				rules = DocRulesSection(UserDB(T.DB, uid), "codewriter")
			}
			return BuildDocAssistPrompt(req.String("name"), req.String("section"), req.Draft, req.String("context"), req.Mode) + rules, nil
		},
		Parse: func(req assist.Request, raw string) assist.Reply {
			reply, value := SplitDraftReply(raw)
			if section := req.String("section"); value != "" && section != "" {
				value = StripLeadingHeading(value, section)
			}
			return assist.Reply{Reply: reply, Value: value}
		},
	})(w, r)
}

// uniqueReferences drops repeat selections of the same item, keeping the
// first, so the order (writer's sources, then the turn's picks) survives.
func uniqueReferences(refs ReferenceSelections) ReferenceSelections {
	seen := map[ReferenceSelection]bool{}
	out := make(ReferenceSelections, 0, len(refs))
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}
