package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

func (T *AnswerAgent) WebPath() string { return "/answer" }
func (T *AnswerAgent) WebName() string { return "Answer" }
func (T *AnswerAgent) WebDesc() string {
	return "Quick web-researched answers for technical questions and mini how-tos."
}

func (T *AnswerAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	// Framework-rendered Answer at /answer/. Same backend records as
	// legacy — only the page chrome changed. Legacy hand-written UI
	// stays at /answer/legacy until ChatPanel covers every feature.
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handleNewAnswerPage(w, r)
	})
	sub.HandleFunc("/legacy", func(w http.ResponseWriter, r *http.Request) {
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "Answer (Legacy)",
			AppName:  "Answer",
			Prefix:   prefix,
			BodyHTML: answerHTML,
			AppCSS:   answerCSS,
			AppJS:    answerJS,
		}))
	})
	sub.HandleFunc("/legacy/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/legacy", http.StatusMovedPermanently)
	})
	// Legacy API endpoints (still used by /answer/legacy).
	sub.HandleFunc("/api/ask", T.handleAsk)
	sub.HandleFunc("/api/history", T.handleHistory)
	sub.HandleFunc("/api/record/", T.handleRecord)
	sub.HandleFunc("/api/delete/", T.handleDelete)
	sub.HandleFunc("/api/followup/", T.handleFollowup)
	// Framework-API endpoints — backed by the same records, reshaped
	// into the {sessions,messages,send} contract ChatPanel expects.
	sub.HandleFunc("/api/sessions", T.handleSessionsList)
	sub.HandleFunc("/api/sessions/", T.handleSessionGet)
	sub.HandleFunc("/api/sessions/delete/", T.handleSessionDelete)
	sub.HandleFunc("/api/send", T.handleChatSend)
	MountSubMux(mux, prefix, sub)
}

// answerSessionSummary is the shape ChatPanel's sidebar list expects
// (with field overrides Question / Date instead of Title / LastAt set
// in page.go).
type answerSessionSummary struct {
	ID       string `json:"ID"`
	Question string `json:"Question"`
	Date     string `json:"Date"`
}

func (T *AnswerAgent) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if T.DB == nil {
		json.NewEncoder(w).Encode([]answerSessionSummary{})
		return
	}
	udb := userDB(T.DB, r)
	var records []AnswerRecord
	for _, key := range udb.Keys(answerTable) {
		var rec AnswerRecord
		if udb.Get(answerTable, key, &rec) {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Date > records[j].Date })
	out := make([]answerSessionSummary, len(records))
	for i, rec := range records {
		out[i] = answerSessionSummary{ID: rec.ID, Question: rec.Question, Date: rec.Date}
	}
	json.NewEncoder(w).Encode(out)
}

// chatPanelMessage is the wire shape for messages inside a session
// load response — Role + Content, matching what ChatPanel renders.
type chatPanelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// handleSessionGet reshapes an AnswerRecord + SharedRecord into the
// {ID, Question (Title), Date (LastAt), Messages} envelope ChatPanel
// expects when loading a session. The first user message is the
// original question; the first assistant message is the answer with
// any sources appended; followups append in order.
func (T *AnswerAgent) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if T.DB == nil {
		http.Error(w, "no db", http.StatusInternalServerError)
		return
	}
	var rec AnswerRecord
	if !userDB(T.DB, r).Get(answerTable, id, &rec) {
		http.NotFound(w, r)
		return
	}
	var shared SharedRecord
	if SharedDB != nil {
		SharedDB.Get(SharedTable, id, &shared)
	}
	msgs := []chatPanelMessage{
		{Role: "user", Content: rec.Question},
		{Role: "assistant", Content: composeAssistantBody(shared.Answer, shared.Sources)},
	}
	for _, t := range rec.Followups {
		msgs = append(msgs, chatPanelMessage{Role: t.Role, Content: t.Content})
	}
	out := struct {
		ID       string             `json:"ID"`
		Question string             `json:"Question"`
		Date     string             `json:"Date"`
		Messages []chatPanelMessage `json:"Messages"`
	}{
		ID:       rec.ID,
		Question: rec.Question,
		Date:     rec.Date,
		Messages: msgs,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// shortArgs renders a tool-call argument map as a one-line string for
// the UI's tool-call pill. Picks the most descriptive single field
// (query, url, path) when present; otherwise concatenates everything
// up to a length cap so the pill stays readable.
func shortArgs(args map[string]any) string {
	for _, key := range []string{"query", "url", "path", "expression", "term", "topic"} {
		if v, ok := args[key]; ok {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" {
				return clip(s, 120)
			}
		}
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return clip(strings.Join(parts, ", "), 120)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// composeAssistantBody appends a numbered Sources block to the answer
// text so ChatPanel renders sources inline (no separate component).
// Any LLM-written `## Sources` section is stripped first so the
// orchestrator's structured Sources map is the single source of truth
// — otherwise both render and the user sees the same citations twice.
func composeAssistantBody(answer string, sources map[int]string) string {
	answer = strings.TrimSpace(StripSourcesSection(answer))
	if len(sources) == 0 {
		return answer
	}
	var b strings.Builder
	b.WriteString(answer)
	b.WriteString("\n\n## Sources\n")
	// Stable order by citation number.
	keys := make([]int, 0, len(sources))
	for n := range sources {
		keys = append(keys, n)
	}
	sort.Ints(keys)
	for _, n := range keys {
		b.WriteString("[")
		b.WriteString(itoa(n))
		b.WriteString("] ")
		b.WriteString(sources[n])
		b.WriteString("\n")
	}
	return b.String()
}

func (T *AnswerAgent) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/delete/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if T.DB == nil {
		http.Error(w, "no db", http.StatusInternalServerError)
		return
	}
	udb := userDB(T.DB, r)
	var rec AnswerRecord
	if !udb.Get(answerTable, id, &rec) {
		http.NotFound(w, r)
		return
	}
	udb.Unset(answerTable, id)
	w.WriteHeader(http.StatusNoContent)
}

// chatSendRequest is the body ChatPanel POSTs to SendURL.
type chatSendRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// handleChatSend is the unified initial+followup endpoint. Empty
// session_id starts a new orchestrator run (the answer becomes the
// session's first assistant message). A populated session_id routes to
// a followup turn against that record.
//
// Streamed via SSE in the format ChatPanel parses:
//   - {type: "session", id} on new-session creation
//   - {type: "chunk",   text}   for streaming content
//   - {type: "done"}             at end
//   - {type: "error",  message} on failure
func (T *AnswerAgent) handleChatSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req chatSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	stopKA := sse.StartKeepalive(10 * time.Second)
	defer stopKA()

	if req.SessionID == "" {
		T.runInitialSend(r, sse, req.Message)
		return
	}
	T.runFollowupSend(r, sse, req.SessionID, req.Message)
}

func (T *AnswerAgent) runInitialSend(r *http.Request, sse *SSEWriter, question string) {
	user := AuthCurrentUser(r)
	udb := userDB(T.DB, r)
	emit := func(status string) {
		sse.SendNamed("status", map[string]string{"text": status})
	}
	// onStep surfaces per-round activity to ChatPanel: each tool the
	// LLM calls becomes a tool_call pill, then a tool_result line when
	// the call returns. Without this the user sees only "Researching..."
	// for the entire orchestrator run, even when it does 8+ tool calls.
	onStep := func(step StepInfo) {
		for _, tc := range step.ToolCalls {
			sse.SendNamed("tool_call", map[string]interface{}{
				"name": tc.Name,
				"args": shortArgs(tc.Args),
			})
		}
	}
	result, runErr := RunOrchestrator(r.Context(), &T.AppCore, udb, user, question, emit, onStep)
	if runErr != nil {
		sse.SendNamed("error", map[string]string{"message": runErr.Error()})
		return
	}

	id := UUIDv4()
	now := time.Now().Format(time.RFC3339)
	rec := AnswerRecord{ID: id, Question: question, Topic: result.Topic, Date: now}
	if udb != nil {
		udb.Set(answerTable, id, rec)
	}
	shared := SharedRecord{ID: id, Question: question, Answer: result.Answer, Sources: result.Sources, Date: now}
	if SharedDB != nil {
		SharedDB.Set(SharedTable, id, shared)
	}
	if KnowledgeDB != nil && strings.TrimSpace(result.Answer) != "" {
		go IngestReport(context.Background(), KnowledgeDB, "answer", id, result.Answer)
	}

	// session event so ChatPanel's sidebar adopts the new ID.
	sse.SendNamed("session", map[string]string{"id": id})
	// Stream the composed answer body as one chunk (orchestrator
	// returns the full text — no incremental streaming there yet).
	sse.SendNamed("chunk", map[string]string{"text": composeAssistantBody(result.Answer, result.Sources)})
	sse.SendNamed("done", map[string]interface{}{})
}

func (T *AnswerAgent) runFollowupSend(r *http.Request, sse *SSEWriter, id, msg string) {
	udb := userDB(T.DB, r)
	user := AuthCurrentUser(r)
	var rec AnswerRecord
	if !udb.Get(answerTable, id, &rec) {
		sse.SendNamed("error", map[string]string{"message": "session not found"})
		return
	}
	var shared SharedRecord
	if SharedDB != nil {
		SharedDB.Get(SharedTable, id, &shared)
	}

	priorFacts := FactsForTopic(udb, user, rec.Topic)
	systemPrompt := buildFollowupSystem(rec, shared, priorFacts)

	history := []Message{}
	for _, t := range rec.Followups {
		history = append(history, Message{Role: t.Role, Content: t.Content})
	}
	history = append(history, Message{Role: "user", Content: msg})

	tools := orchestratorTools(udb, user, rec.Topic)
	var fullReply strings.Builder
	handler := func(chunk string) {
		fullReply.WriteString(chunk)
		sse.SendNamed("chunk", map[string]string{"text": chunk})
	}
	onStep := func(step StepInfo) {
		for _, tc := range step.ToolCalls {
			sse.SendNamed("tool_call", map[string]interface{}{
				"name": tc.Name,
				"args": shortArgs(tc.Args),
			})
		}
	}
	resp, _, err := T.AppCore.RunAgentLoop(r.Context(), history, AgentLoopConfig{
		SystemPrompt: systemPrompt,
		Tools:        tools,
		MaxRounds:    16,
		RouteKey:     "app.answer",
		Stream:       handler,
		OnStep:       onStep,
	})
	if err != nil {
		sse.SendNamed("error", map[string]string{"message": err.Error()})
		return
	}
	reply := strings.TrimSpace(fullReply.String())
	if reply == "" && resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}

	now := time.Now().Format(time.RFC3339)
	rec.Followups = append(rec.Followups,
		FollowupTurn{Role: "user", Content: msg, Date: now},
		FollowupTurn{Role: "assistant", Content: reply, Date: now},
	)
	udb.Set(answerTable, id, rec)

	sse.SendNamed("done", map[string]interface{}{})
}

// AnswerEvent is streamed to the browser over SSE.
type AnswerEvent struct {
	Type    string         `json:"type"`
	Status  string         `json:"status,omitempty"`
	Answer  string         `json:"answer,omitempty"`
	Sources map[int]string `json:"sources,omitempty"`
	ID      string         `json:"id,omitempty"`
}

func (T *AnswerAgent) handleAsk(w http.ResponseWriter, r *http.Request) {
	question := strings.TrimSpace(r.URL.Query().Get("q"))
	if question == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	stopKA := sse.StartKeepalive(10 * time.Second)
	defer stopKA()

	emit := func(status string) {
		sse.Send(AnswerEvent{Type: "status", Status: status})
	}

	user := AuthCurrentUser(r)
	udb := userDB(T.DB, r)
	result, runErr := RunOrchestrator(r.Context(), &T.AppCore, udb, user, question, emit, nil)
	if runErr != nil {
		sse.Send(AnswerEvent{Type: "error", Status: runErr.Error()})
		return
	}

	id := UUIDv4()
	now := time.Now().Format(time.RFC3339)

	// Per-user index row — drives the sidebar and follow-ups.
	rec := AnswerRecord{
		ID:       id,
		Question: question,
		Topic:    result.Topic,
		Date:     now,
	}
	if udb != nil {
		udb.Set(answerTable, id, rec)
	}

	// Deployment-wide shared body — keyed by the same ID so
	// search_knowledge / get_report can resolve it without knowing
	// which user originated the question.
	shared := SharedRecord{
		ID:       id,
		Question: question,
		Answer:   result.Answer,
		Sources:  result.Sources,
		Date:     now,
	}
	if SharedDB != nil {
		SharedDB.Set(SharedTable, id, shared)
	}

	// Index into the central knowledge vector store so chat's
	// search_knowledge tool can find this answer. Async — embedding
	// can take a moment and the client doesn't need to wait.
	if KnowledgeDB != nil && strings.TrimSpace(result.Answer) != "" {
		go IngestReport(context.Background(), KnowledgeDB, "answer", id, result.Answer)
	}

	sse.Send(AnswerEvent{
		Type:    "done",
		Answer:  result.Answer,
		Sources: result.Sources,
		ID:      id,
	})
}

func (T *AnswerAgent) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if T.DB == nil {
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	udb := userDB(T.DB, r)
	var records []AnswerRecord
	for _, key := range udb.Keys(answerTable) {
		var rec AnswerRecord
		if udb.Get(answerTable, key, &rec) {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Date > records[j].Date
	})
	type summary struct {
		ID       string `json:"ID"`
		Question string `json:"Question"`
		Date     string `json:"Date"`
	}
	out := make([]summary, len(records))
	for i, rec := range records {
		out[i] = summary{ID: rec.ID, Question: rec.Question, Date: rec.Date}
	}
	json.NewEncoder(w).Encode(out)
}

func (T *AnswerAgent) handleRecord(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/record/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if T.DB == nil {
		http.Error(w, "no db", http.StatusInternalServerError)
		return
	}
	var rec AnswerRecord
	if !userDB(T.DB, r).Get(answerTable, id, &rec) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Body lives in the deployment-wide shared table; merge for the
	// API response so the UI sees one record shape.
	var shared SharedRecord
	if SharedDB != nil {
		SharedDB.Get(SharedTable, id, &shared)
	}
	out := struct {
		ID        string         `json:"ID"`
		Question  string         `json:"Question"`
		Answer    string         `json:"Answer"`
		Sources   map[int]string `json:"Sources,omitempty"`
		Topic     string         `json:"Topic,omitempty"`
		Date      string         `json:"Date"`
		Archived  bool           `json:"Archived,omitempty"`
		Followups []FollowupTurn `json:"Followups,omitempty"`
	}{
		ID:        rec.ID,
		Question:  rec.Question,
		Answer:    shared.Answer,
		Sources:   shared.Sources,
		Topic:     rec.Topic,
		Date:      rec.Date,
		Archived:  rec.Archived,
		Followups: rec.Followups,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleFollowup runs a follow-up chat turn against an existing
// answer record. The original question, answer, sources, topic, and
// any saved facts are loaded as context. New turns are persisted on
// the record so the conversation builds across sessions.
//
// Streamed via SSE: chunk events for content, done at end.
func (T *AnswerAgent) handleFollowup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/followup/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(r.URL.Query().Get("m"))
	if msg == "" {
		http.Error(w, "m (message) required", http.StatusBadRequest)
		return
	}
	if T.DB == nil {
		http.Error(w, "no db", http.StatusInternalServerError)
		return
	}

	udb := userDB(T.DB, r)
	user := AuthCurrentUser(r)
	var rec AnswerRecord
	if !udb.Get(answerTable, id, &rec) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var shared SharedRecord
	if SharedDB != nil {
		SharedDB.Get(SharedTable, id, &shared)
	}

	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	stopKA := sse.StartKeepalive(10 * time.Second)
	defer stopKA()

	// Build the context: original Q&A, sources, prior follow-ups.
	// Facts under the topic come into the system prompt so the
	// follow-up agent can lean on the orchestrator's prior research.
	priorFacts := FactsForTopic(udb, user, rec.Topic)
	systemPrompt := buildFollowupSystem(rec, shared, priorFacts)

	// Replay prior follow-up turns as messages so the agent has the
	// full conversation history.
	history := []Message{}
	for _, t := range rec.Followups {
		history = append(history, Message{Role: t.Role, Content: t.Content})
	}
	history = append(history, Message{Role: "user", Content: msg})

	// Follow-up agent gets the same tool set the orchestrator had:
	// web_search / fetch_url / browse_page / screenshot_page for
	// fresh research, plus the fact tool to read/write the topic's
	// knowledge base. Multi-step follow-ups (search → fetch → cite)
	// work the same as the initial answer; the agent decides when
	// the original answer covers it vs. when more digging is needed.
	tools := orchestratorTools(udb, user, rec.Topic)
	var fullReply strings.Builder
	handler := func(chunk string) {
		fullReply.WriteString(chunk)
		sse.Send(map[string]string{"type": "chunk", "text": chunk})
	}
	resp, _, err := T.AppCore.RunAgentLoop(r.Context(), history, AgentLoopConfig{
		SystemPrompt: systemPrompt,
		Tools:        tools,
		MaxRounds:    16,
		RouteKey:     "app.answer",
		Stream:       handler,
		ChatOptions:  nil,
	})
	if err != nil {
		sse.Send(map[string]string{"type": "error", "message": err.Error()})
		return
	}
	reply := strings.TrimSpace(fullReply.String())
	if reply == "" && resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}

	// Persist both the user turn and the assistant reply on the record.
	now := time.Now().Format(time.RFC3339)
	rec.Followups = append(rec.Followups,
		FollowupTurn{Role: "user", Content: msg, Date: now},
		FollowupTurn{Role: "assistant", Content: reply, Date: now},
	)
	udb.Set(answerTable, id, rec)

	sse.Send(map[string]string{"type": "done"})
}

// buildFollowupSystem renders the system prompt for follow-up chats.
// Includes the original Q&A, source list, and any topic facts so the
// follow-up agent has the same context the orchestrator did. The
// agent has the full research tool set (web_search, fetch_url,
// browse_page, screenshot_page) plus save_fact, so it can do fresh
// research when a follow-up needs info beyond the original answer.
func buildFollowupSystem(rec AnswerRecord, shared SharedRecord, facts []AnswerFact) string {
	var b strings.Builder
	b.WriteString("You are a follow-up assistant for a research answer the user already received. Answer questions about the original answer's content, drill into specific details, clarify points, or apply the knowledge to the user's specific situation.\n\n")
	b.WriteString("WORKFLOW:\n")
	b.WriteString("1. If the original answer + saved facts already cover the follow-up, answer directly from them. Cite specific sources/facts.\n")
	b.WriteString("2. If the follow-up needs information NOT in the original answer (a related detail, a more recent update, a specific edge case), use web_search / fetch_url / browse_page to research it. Don't be shy about doing fresh research — that's what these tools are for.\n")
	b.WriteString("3. As you discover durable, factual additions during follow-up research, call save_fact to record them under this topic. Future questions benefit. Don't save speculation, opinions, or rapidly-changing data.\n")
	b.WriteString("4. Cite new sources with [N] inline + a short numbered list at the end if you fetched anything new.\n\n")
	b.WriteString("ORIGINAL QUESTION:\n")
	b.WriteString(rec.Question)
	b.WriteString("\n\nORIGINAL ANSWER:\n")
	b.WriteString(shared.Answer)
	if len(shared.Sources) > 0 {
		b.WriteString("\n\nSOURCES:\n")
		for n, url := range shared.Sources {
			b.WriteString("[")
			b.WriteString(itoa(n))
			b.WriteString("] ")
			b.WriteString(url)
			b.WriteString("\n")
		}
	}
	if len(facts) > 0 {
		b.WriteString("\nKNOWN FACTS UNDER TOPIC \"")
		b.WriteString(rec.Topic)
		b.WriteString("\":\n")
		b.WriteString(FormatFactsForPrompt(facts))
	} else {
		b.WriteString("\nKNOWN FACTS: none yet for this topic. Save anything new the conversation surfaces.\n")
	}
	return b.String()
}

func itoa(n int) string {
	// Avoid pulling strconv just for this.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (T *AnswerAgent) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/delete/")
	if id == "" || T.DB == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	userDB(T.DB, r).Unset(answerTable, id)
	// Cascade: drop the shared body and any embedded chunks so the
	// answer disappears from search_knowledge too. Each user's record
	// has its own shared partner (no dedupe across users), so the
	// delete is unconditional — no refcount needed.
	if SharedDB != nil {
		SharedDB.Unset(SharedTable, id)
	}
	if KnowledgeDB != nil {
		DeleteReportChunks(KnowledgeDB, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func userDB(db Database, r *http.Request) Database {
	username := AuthCurrentUser(r)
	if username == "" {
		username = "default"
	}
	return db.Sub("answer_" + username)
}

// ---- static assets ----

const answerCSS = `
body { background:#0d1117; color:#c9d1d9; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif; }

#app { display:flex; height:100vh; overflow:hidden; }

/* sidebar */
#sidebar {
  width:260px; min-width:200px; max-width:340px;
  background:#161b22; border-right:1px solid #30363d;
  display:flex; flex-direction:column; overflow:hidden;
}
#sidebar-header {
  padding:0.75rem 1rem; border-bottom:1px solid #30363d;
  display:flex; align-items:center; justify-content:space-between;
}
#sidebar-header h2 { margin:0; font-size:0.9rem; color:#c9d1d9; font-weight:600; }
.sidebar-sort { display:flex; gap:2px; }
.sidebar-sort-btn {
  background:#0d1117; border:1px solid #30363d; color:#8b949e;
  font-size:0.7rem; padding:2px 7px; border-radius:4px; cursor:pointer;
}
.sidebar-sort-btn.active { background:#388bfd; border-color:#388bfd; color:#fff; }
#history-list { flex:1; overflow-y:auto; padding:0.5rem; }
.history-item {
  padding:0.5rem 0.6rem; border-radius:6px; cursor:pointer;
  border:1px solid transparent; margin-bottom:0.3rem;
  display:flex; align-items:flex-start; gap:0.4rem;
}
.history-item:hover { background:#1c2128; border-color:#30363d; }
.history-item.active { background:#1c2128; border-color:#388bfd; }
.history-item .hq { font-size:0.8rem; color:#c9d1d9; flex:1; line-height:1.35; }
.history-item .hd { font-size:0.7rem; color:#484f58; white-space:nowrap; }
.history-item .hdel {
  background:none; border:none; color:#484f58; cursor:pointer;
  font-size:0.75rem; padding:0 2px; line-height:1;
}
.history-item .hdel:hover { color:#f85149; }
#history-empty { color:#484f58; font-size:0.82rem; text-align:center; padding:1rem 0.5rem; }

/* main */
#main { flex:1; display:flex; flex-direction:column; overflow:hidden; }

/* question bar */
#question-bar {
  padding:1rem 1.25rem; border-bottom:1px solid #30363d;
  background:#161b22; display:flex; gap:0.5rem; align-items:flex-end;
}
#question-input {
  flex:1; background:#0d1117; border:1px solid #30363d; border-radius:8px;
  color:#c9d1d9; font-size:0.9rem; padding:0.55rem 0.75rem;
  resize:none; outline:none; font-family:inherit; min-height:40px; max-height:120px;
}
#question-input:focus { border-color:#388bfd; }
#question-input:disabled { opacity:0.45; cursor:not-allowed; }
#ask-btn {
  background:#388bfd; color:#fff; border:none; border-radius:8px;
  padding:0.55rem 1.1rem; font-size:0.88rem; font-weight:600;
  cursor:pointer; white-space:nowrap; height:40px;
}
#ask-btn:hover { background:#58a6ff; }
#ask-btn.cancel { background:#b91c1c; }
#ask-btn.cancel:hover { background:#ef4444; }

/* status */
#status-line {
  padding:0.3rem 1.25rem; font-size:0.78rem; color:#8b949e; min-height:1.5rem;
  display:flex; align-items:center; gap:0.4rem;
}
#status-line.active { border-bottom:1px solid #30363d; }
.ans-spinner {
  display:none; width:12px; height:12px; flex-shrink:0;
  border:2px solid #30363d; border-top-color:#388bfd;
  border-radius:50%; animation:ans-spin 0.7s linear infinite;
}
.ans-spinner.running { display:inline-block; }
@keyframes ans-spin { to { transform:rotate(360deg); } }

/* answer area */
#answer-area { flex:1; overflow-y:auto; padding:1.5rem 1.25rem; }
#answer-placeholder { color:#484f58; font-size:0.9rem; text-align:center; margin-top:3rem; }
#answer-content { max-width:820px; }
#answer-question { font-size:1.05rem; font-weight:600; color:#e6edf3; margin-bottom:1rem; line-height:1.4; }
.answer-body { line-height:1.7; font-size:0.9rem; }
.answer-body h1,.answer-body h2,.answer-body h3 { color:#e6edf3; margin:1rem 0 0.5rem; }
.answer-body p { margin:0 0 0.75rem; }
.answer-body ul,.answer-body ol { margin:0 0 0.75rem; padding-left:1.5rem; }
.answer-body li { margin-bottom:0.3rem; }
.answer-body code {
  background:#1c2128; border:1px solid #30363d; border-radius:4px;
  padding:1px 5px; font-size:0.85em; font-family:'Fira Code',monospace;
}
.answer-body pre {
  background:#1c2128; border:1px solid #30363d; border-radius:6px;
  padding:0.75rem 1rem; overflow-x:auto; margin:0 0 0.75rem;
}
.answer-body pre code { background:none; border:none; padding:0; }
.answer-body a { color:#58a6ff; }
.answer-body strong { color:#e6edf3; }
.answer-body table { border-collapse:collapse; margin:0.75rem 0; width:100%; font-size:0.85rem; }
.answer-body th, .answer-body td { border:1px solid #30363d; padding:0.35rem 0.75rem; text-align:left; }
.answer-body th { background:#1c2128; color:#e6edf3; font-weight:600; }
.answer-body tr:nth-child(even) td { background:#161b22; }
.answer-body blockquote { border-left:3px solid #30363d; margin:0 0 0.75rem; padding:0.25rem 0.75rem; color:#8b949e; }

/* sources */
#sources-section { margin-top:1.25rem; padding-top:1rem; border-top:1px solid #30363d; }
#sources-section h4 { font-size:0.82rem; color:#8b949e; margin:0 0 0.5rem; font-weight:600; text-transform:uppercase; letter-spacing:0.05em; }
#sources-list { list-style:none; padding:0; margin:0; display:flex; flex-direction:column; gap:0.3rem; }
#sources-list li { display:flex; align-items:baseline; gap:0.4rem; font-size:0.8rem; }
.src-num { color:#484f58; min-width:1.5rem; font-size:0.75rem; }
#sources-list a { color:#58a6ff; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; max-width:480px; }
#sources-list a:hover { text-decoration:underline; }
.src-ref { color:#58a6ff; text-decoration:none; font-size:0.8em; vertical-align:super; }
.src-ref:hover { text-decoration:underline; }

/* follow-up chat */
#followup-section { margin-top:1.5rem; padding-top:1rem; border-top:1px solid #30363d; max-width:820px; }
#followup-section h4 { font-size:0.82rem; color:#8b949e; margin:0 0 0.6rem; font-weight:600; text-transform:uppercase; letter-spacing:0.05em; }
#followup-thread { display:flex; flex-direction:column; gap:0.6rem; margin-bottom:0.75rem; }
.followup-msg { padding:0.5rem 0.75rem; border-radius:8px; font-size:0.88rem; line-height:1.55; }
.followup-msg.user { background:#1c2128; border:1px solid #30363d; color:#c9d1d9; }
.followup-msg.assistant { background:#161b22; border:1px solid #30363d; color:#c9d1d9; }
.followup-msg .role { font-size:0.7rem; text-transform:uppercase; color:#8b949e; letter-spacing:0.05em; margin-bottom:0.3rem; font-weight:600; }
.followup-msg.user .role { color:#58a6ff; }
.followup-msg.assistant .role { color:#3fb950; }
.followup-msg .body { white-space:pre-wrap; }
.followup-msg .body p { margin:0 0 0.5rem; }
.followup-msg .body p:last-child { margin-bottom:0; }
.followup-msg .body.streaming::after {
  content:''; display:inline-block; width:10px; height:10px; margin-left:0.4rem;
  border:2px solid #30363d; border-top-color:#388bfd;
  border-radius:50%; animation:ans-spin 0.7s linear infinite; vertical-align:middle;
}
.followup-msg .body.thinking-only {
  display:flex; align-items:center; gap:0.5rem; color:#8b949e; font-size:0.82rem; font-style:italic;
}
.followup-msg .body.thinking-only::before {
  content:''; display:inline-block; width:12px; height:12px;
  border:2px solid #30363d; border-top-color:#388bfd;
  border-radius:50%; animation:ans-spin 0.7s linear infinite;
}
#followup-input-row { display:flex; gap:0.5rem; align-items:flex-end; }
#followup-input {
  flex:1; background:#0d1117; border:1px solid #30363d; border-radius:8px;
  color:#c9d1d9; font-size:0.88rem; padding:0.5rem 0.7rem;
  resize:none; outline:none; font-family:inherit; min-height:36px; max-height:100px;
}
#followup-input:focus { border-color:#388bfd; }
#followup-send {
  background:#388bfd; color:#fff; border:none; border-radius:8px;
  padding:0.5rem 0.9rem; font-size:0.85rem; font-weight:600;
  cursor:pointer; height:36px;
}
#followup-send:hover { background:#58a6ff; }
#followup-send:disabled { opacity:0.45; cursor:not-allowed; }

@media (max-width:640px) {
  #app { flex-direction:column; }
  #sidebar { width:100%; max-width:100%; height:auto; max-height:40vh; border-right:none; border-bottom:1px solid #30363d; }
  #main { min-height:60vh; }
}
`

const answerJS = `
var historyItems = [];
var historySort = 'date';
var currentID = '';
var currentSSE = null;

function init() {
  var inp = document.getElementById('question-input');
  inp.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); askQuestion(); }
  });
  inp.addEventListener('input', autoGrow);
  // Follow-up input: Enter sends, Shift+Enter newline. Auto-grow.
  var fup = document.getElementById('followup-input');
  if (fup) {
    fup.addEventListener('keydown', function(e) {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendFollowup(); }
    });
    fup.addEventListener('input', function() {
      fup.style.height = 'auto';
      fup.style.height = Math.min(fup.scrollHeight, 100) + 'px';
    });
  }
  loadHistory();
}

function autoGrow() {
  var el = document.getElementById('question-input');
  el.style.height = 'auto';
  el.style.height = Math.min(el.scrollHeight, 120) + 'px';
}

function setRunning(on) {
  var btn = document.getElementById('ask-btn');
  var inp = document.getElementById('question-input');
  var sp = document.getElementById('answer-spinner');
  if (on) {
    btn.textContent = 'Cancel';
    btn.className = 'cancel';
    btn.onclick = cancelAnswer;
    inp.disabled = true;
    sp.className = 'ans-spinner running';
  } else {
    btn.textContent = 'Ask';
    btn.className = '';
    btn.onclick = askQuestion;
    inp.disabled = false;
    sp.className = 'ans-spinner';
  }
}

function cancelAnswer() {
  if (currentSSE) { currentSSE.close(); currentSSE = null; }
  setRunning(false);
  setStatus('Cancelled.', false);
}

function askQuestion() {
  var q = document.getElementById('question-input').value.trim();
  if (!q) return;
  if (currentSSE) { currentSSE.close(); currentSSE = null; }

  setRunning(true);
  document.getElementById('answer-placeholder').style.display = 'none';
  document.getElementById('answer-content').style.display = 'block';
  document.getElementById('answer-question').textContent = q;
  document.getElementById('answer-body').innerHTML = '';
  document.getElementById('sources-section').style.display = 'none';
  document.getElementById('followup-section').style.display = 'none';
  document.getElementById('followup-thread').innerHTML = '';
  setStatus('Searching...', true);
  currentID = '';

  currentSSE = new EventSource('api/ask?q=' + encodeURIComponent(q));
  currentSSE.onmessage = function(e) {
    var ev = JSON.parse(e.data);
    if (ev.type === 'status') {
      setStatus(ev.status, true);
    } else if (ev.type === 'done') {
      currentSSE.close(); currentSSE = null;
      setRunning(false);
      setStatus('', false);
      currentID = ev.id || '';
      renderAnswer(ev.answer || '', ev.sources || {});
      loadHistory();
    } else if (ev.type === 'error') {
      currentSSE.close(); currentSSE = null;
      setRunning(false);
      setStatus('Error: ' + (ev.status || 'unknown error'), false);
    }
  };
  currentSSE.onerror = function() {
    if (currentSSE) { currentSSE.close(); currentSSE = null; }
    setRunning(false);
    setStatus('Connection lost.', false);
  };
}

function linkifyCitations(html, sources) {
  // Split on pre/code blocks so we don't linkify inside them.
  var parts = html.split(/(<(?:pre|code)[^>]*>[\s\S]*?<\/(?:pre|code)>)/i);
  for (var i = 0; i < parts.length; i += 2) {
    parts[i] = parts[i].replace(/\[(\d+(?:,\s*\d+)*)\]/g, function(match, inner) {
      var nums = inner.split(',').map(function(s) { return s.trim(); });
      if (nums.length === 1) {
        var url = sources[nums[0]];
        if (url) return '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener" class="src-ref">' + match + '</a>';
        return match;
      }
      var linked = nums.map(function(n) {
        var url = sources[n];
        if (url) return '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener" class="src-ref">' + n + '</a>';
        return n;
      });
      return '[' + linked.join(', ') + ']';
    });
  }
  return parts.join('');
}

function renderAnswer(answer, sources) {
  document.getElementById('answer-body').innerHTML = '<div class="answer-body">' + linkifyCitations(renderMarkdown(answer), sources) + '</div>';
  var sect = document.getElementById('sources-section');
  var list = document.getElementById('sources-list');
  list.innerHTML = '';
  var keys = Object.keys(sources).map(Number).sort(function(a, b) { return a - b; });
  if (keys.length === 0) {
    sect.style.display = 'none';
  } else {
    for (var i = 0; i < keys.length; i++) {
      var n = keys[i];
      var url = sources[String(n)] || sources[n] || '';
      if (!url) continue;
      var domain = url.replace(/^https?:\/\//, '').split('/')[0];
      var li = document.createElement('li');
      li.innerHTML = '<span class="src-num">[' + n + ']</span>'
        + '<a href="' + escapeHtml(url) + '" target="_blank" rel="noopener">' + escapeHtml(domain) + '</a>';
      list.appendChild(li);
    }
    sect.style.display = 'block';
  }
  // Show follow-up section once an answer exists. Thread populated
  // separately by renderFollowups (called when loading an existing
  // record, or empty for a freshly-asked question).
  document.getElementById('followup-section').style.display = 'block';
}

function renderFollowups(turns) {
  var thread = document.getElementById('followup-thread');
  thread.innerHTML = '';
  if (!turns || !turns.length) return;
  for (var i = 0; i < turns.length; i++) {
    appendFollowupTurn(turns[i].role, turns[i].content);
  }
}

function appendFollowupTurn(role, content) {
  var thread = document.getElementById('followup-thread');
  var div = document.createElement('div');
  div.className = 'followup-msg ' + role;
  div.innerHTML = '<div class="role">' + (role === 'user' ? 'You' : 'Assistant') + '</div>'
    + '<div class="body">' + (role === 'assistant' && typeof renderMarkdown === 'function' ? renderMarkdown(content) : escapeHtml(content)) + '</div>';
  thread.appendChild(div);
  // Scroll the answer pane to keep the latest message visible.
  var area = document.getElementById('answer-area');
  area.scrollTop = area.scrollHeight;
  return div;
}

var followupSSE = null;

function sendFollowup() {
  if (!currentID) {
    alert('Save an answer first by asking a question.');
    return;
  }
  var inp = document.getElementById('followup-input');
  var msg = inp.value.trim();
  if (!msg) return;
  if (followupSSE) { followupSSE.close(); followupSSE = null; }

  // Append the user turn immediately for snappy feedback. Server will
  // also persist it; on the next loadRecord call the same turn will
  // render from the persisted history.
  appendFollowupTurn('user', msg);
  inp.value = '';
  inp.style.height = 'auto';

  // Create an empty assistant bubble with a "thinking..." spinner.
  // The spinner stays until the first chunk arrives; then we switch
  // to streaming mode (text + trailing cursor spinner).
  var assistantEl = appendFollowupTurn('assistant', '');
  var bodyEl = assistantEl.querySelector('.body');
  bodyEl.className = 'body thinking-only';
  bodyEl.textContent = 'thinking…';

  var sendBtn = document.getElementById('followup-send');
  sendBtn.disabled = true;
  inp.disabled = true;

  var streamed = '';
  var firstChunk = true;
  followupSSE = new EventSource('api/followup/' + encodeURIComponent(currentID) + '?m=' + encodeURIComponent(msg));
  followupSSE.onmessage = function(e) {
    var ev = JSON.parse(e.data);
    if (ev.type === 'chunk') {
      if (firstChunk) {
        // First content arrives — swap from "thinking" spinner to
        // streaming mode (text + small trailing spinner).
        bodyEl.className = 'body streaming';
        bodyEl.textContent = '';
        firstChunk = false;
      }
      streamed += ev.text;
      bodyEl.textContent = streamed;
      var area = document.getElementById('answer-area');
      area.scrollTop = area.scrollHeight;
    } else if (ev.type === 'done') {
      followupSSE.close(); followupSSE = null;
      // Drop streaming spinner; final markdown render.
      bodyEl.className = 'body';
      if (typeof renderMarkdown === 'function' && streamed) {
        bodyEl.innerHTML = renderMarkdown(streamed);
      } else if (!streamed) {
        // No content ever arrived; show a fallback so the bubble
        // isn't empty.
        bodyEl.textContent = '(no response)';
      }
      sendBtn.disabled = false;
      inp.disabled = false;
      inp.focus();
    } else if (ev.type === 'error') {
      followupSSE.close(); followupSSE = null;
      bodyEl.className = 'body';
      bodyEl.textContent = '[error] ' + (ev.message || 'unknown error');
      sendBtn.disabled = false;
      inp.disabled = false;
    }
  };
  followupSSE.onerror = function() {
    if (followupSSE) { followupSSE.close(); followupSSE = null; }
    bodyEl.className = 'body';
    if (firstChunk) bodyEl.textContent = '[connection lost]';
    sendBtn.disabled = false;
    inp.disabled = false;
  };
}

function setStatus(msg, active) {
  var el = document.getElementById('status-line');
  document.getElementById('status-text').textContent = msg;
  el.className = active ? 'active' : '';
}

function loadHistory() {
  fetch('api/history').then(function(r) { return r.json(); }).then(function(items) {
    historyItems = items || [];
    renderHistory();
  });
}

function setHistorySort(s) {
  historySort = s;
  document.getElementById('hsort-date').className = 'sidebar-sort-btn' + (s === 'date' ? ' active' : '');
  document.getElementById('hsort-name').className = 'sidebar-sort-btn' + (s === 'name' ? ' active' : '');
  renderHistory();
}

function renderHistory() {
  var list = document.getElementById('history-list');
  var empty = document.getElementById('history-empty');
  if (!historyItems || historyItems.length === 0) {
    list.innerHTML = '';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';
  var sorted = historyItems.slice();
  if (historySort === 'name') {
    sorted.sort(function(a, b) { return (a.Question || '').localeCompare(b.Question || ''); });
  } else {
    sorted.sort(function(a, b) { return (b.Date || '') < (a.Date || '') ? -1 : 1; });
  }
  var html = '';
  for (var i = 0; i < sorted.length; i++) {
    var it = sorted[i];
    var active = it.ID === currentID ? ' active' : '';
    var dateStr = it.Date ? new Date(it.Date).toLocaleDateString() : '';
    html += '<div class="history-item' + active + '" data-id="' + escapeHtml(it.ID) + '" onclick="loadRecord(\'' + escapeHtml(it.ID) + '\')">'
      + '<div class="hq">' + escapeHtml(it.Question) + '</div>'
      + '<div style="display:flex;flex-direction:column;align-items:flex-end;gap:2px">'
      + '<span class="hd">' + escapeHtml(dateStr) + '</span>'
      + '<button class="hdel" onclick="event.stopPropagation();deleteRecord(\'' + escapeHtml(it.ID) + '\')" title="Delete">&times;</button>'
      + '</div></div>';
  }
  list.innerHTML = html;
}

function loadRecord(id) {
  fetch('api/record/' + id).then(function(r) { return r.json(); }).then(function(rec) {
    currentID = rec.ID;
    document.getElementById('question-input').value = rec.Question;
    autoGrow();
    document.getElementById('answer-placeholder').style.display = 'none';
    document.getElementById('answer-content').style.display = 'block';
    document.getElementById('answer-question').textContent = rec.Question;
    renderAnswer(rec.Answer || '', rec.Sources || {});
    renderFollowups(rec.Followups || []);
    renderHistory();
  });
}

function deleteRecord(id) {
  fetch('api/delete/' + id, {method: 'DELETE'}).then(function() {
    historyItems = historyItems.filter(function(it) { return it.ID !== id; });
    if (currentID === id) {
      currentID = '';
      document.getElementById('answer-content').style.display = 'none';
      document.getElementById('answer-placeholder').style.display = 'block';
    }
    renderHistory();
  });
}

window.addEventListener('DOMContentLoaded', init);
`

const answerHTML = `
<div id="app">
  <div id="sidebar">
    <div id="sidebar-header">
      <h2>History</h2>
      <div class="sidebar-sort">
        <button id="hsort-date" class="sidebar-sort-btn active" onclick="setHistorySort('date')">Newest</button>
        <button id="hsort-name" class="sidebar-sort-btn" onclick="setHistorySort('name')">A&#x2013;Z</button>
      </div>
    </div>
    <div id="history-list"></div>
    <div id="history-empty" style="display:none">No history yet.</div>
  </div>
  <div id="main">
    <div id="question-bar">
      <textarea id="question-input" placeholder="Ask a technical question&#x2026;" rows="1"></textarea>
      <button id="ask-btn" onclick="askQuestion()">Ask</button>
    </div>
    <div id="status-line">
      <span id="answer-spinner" class="ans-spinner"></span>
      <span id="status-text"></span>
    </div>
    <div id="answer-area">
      <div id="answer-placeholder">Ask a question to get a quick, web-researched answer.</div>
      <div id="answer-content" style="display:none">
        <div id="answer-question"></div>
        <div id="answer-body"></div>
        <div id="sources-section" style="display:none">
          <h4>Sources</h4>
          <ul id="sources-list"></ul>
        </div>
        <div id="followup-section" style="display:none">
          <h4>Follow-up</h4>
          <div id="followup-thread"></div>
          <div id="followup-input-row">
            <textarea id="followup-input" placeholder="Ask a follow-up about this answer&#x2026;" rows="1"></textarea>
            <button id="followup-send" onclick="sendFollowup()">Send</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>
`
