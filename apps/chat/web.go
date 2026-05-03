package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

func (T *ChatAgent) WebPath() string { return "/chat" }
func (T *ChatAgent) WebName() string { return "Chat" }
func (T *ChatAgent) WebDesc() string {
	return "Chat with a tool-equipped LLM."
}

func (T *ChatAgent) RegisterRoutes(mux *http.ServeMux, prefix string) {
	sub := http.NewServeMux()
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "Chat",
			AppName:  "Chat",
			Prefix:   prefix,
			BodyHTML: chatBody,
			AppCSS:   chatCSS,
			AppJS:    chatJS,
		}))
	})
	sub.HandleFunc("/api/send", T.handleSend)
	sub.HandleFunc("/api/tools", T.handleTools)
	sub.HandleFunc("/api/sessions", T.handleSessionsList)
	sub.HandleFunc("/api/sessions/", T.handleSessionGet)
	sub.HandleFunc("/api/sessions/delete/", T.handleSessionDelete)
	sub.HandleFunc("/api/sessions/archive/", T.handleSessionArchive)
	sub.HandleFunc("/api/settings/private", T.handlePrivateModeGet)
	sub.HandleFunc("/api/settings/private/set", T.handlePrivateModeSet)
	MountSubMux(mux, prefix, sub)
}

// handleSessionsList returns a lightweight summary of the caller's
// saved chat sessions, most-recent first. The summary omits the full
// Messages slice so the sidebar can page through sessions without
// loading every prior conversation into memory.
func (T *ChatAgent) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	username := sessionUsername(r)
	sessions := ListChatSessionsForUser(T.DB, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SessionSummaries(sessions))
}

// handleSessionGet returns the full session record (including every
// stored Message) so the UI can rehydrate a past conversation when the
// user clicks it in the sidebar. 404s when the caller doesn't own the
// requested session — session access is strictly per-user.
func (T *ChatAgent) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	s, ok := LoadChatSession(T.DB, id)
	if !ok || s.Username != sessionUsername(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// handleSessionDelete removes a session entirely. Only the session's
// owner can delete; mismatched ownership returns 404 so the response
// doesn't reveal whether a given id exists.
func (T *ChatAgent) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/delete/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s, ok := LoadChatSession(T.DB, id)
	if !ok || s.Username != sessionUsername(r) {
		http.NotFound(w, r)
		return
	}
	DeleteChatSession(T.DB, id)
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionArchive flips the Archived flag. Same ownership check
// as delete. Mirrors the research/debate archive pattern so the
// shared webui.historyList component's archive flow works unchanged.
func (T *ChatAgent) handleSessionArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/archive/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s, ok := LoadChatSession(T.DB, id)
	if !ok || s.Username != sessionUsername(r) {
		http.NotFound(w, r)
		return
	}
	s.Archived = !s.Archived
	s.LastAt = time.Now()
	SaveChatSession(T.DB, s)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"archived": s.Archived})
}

// allowedTools returns the registered tool list filtered by the core
// BlockedTools set. Tools that perform real-world side effects are
// blocked from the testing UI to keep it sandboxed.
func allowedTools() []ChatTool {
	return FilterChatTools(BlockedTools)
}

// handleTools returns the list of allowed tool names + descriptions so
// the UI can show the user which tools are available.
func (T *ChatAgent) handleTools(w http.ResponseWriter, r *http.Request) {
	type toolInfo struct {
		Name string `json:"name"`
		Desc string `json:"desc"`
	}
	var out []toolInfo
	if r.URL.Query().Get("private") == "true" {
		for _, t := range FilterChatToolsPrivate() {
			out = append(out, toolInfo{Name: t.Name(), Desc: t.Desc()})
		}
	} else {
		for _, t := range allowedTools() {
			out = append(out, toolInfo{Name: t.Name(), Desc: t.Desc()})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handlePrivateModeGet returns the current user's private-mode preference.
func (T *ChatAgent) handlePrivateModeGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	username := sessionUsername(r)
	mode := AuthGetPrivateMode(T.DB, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"private_mode": mode})
}

// handlePrivateModeSet updates the current user's private-mode preference.
func (T *ChatAgent) handlePrivateModeSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PrivateMode bool `json:"private_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	username := sessionUsername(r)
	AuthSetPrivateMode(T.DB, username, req.PrivateMode)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"private_mode": req.PrivateMode})
}

// chatRequest is the wire format from the frontend.
type chatRequest struct {
	// SessionID ties the message to a persisted ChatSession. Empty =
	// start a new session (server mints an ID and returns it in the
	// first SSE event).
	SessionID string `json:"session_id"`
	History   []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
	Message   string   `json:"message"`
	Tools     []string `json:"tools"` // optional whitelist; empty = all
	PrivateMode bool   `json:"private_mode"`
	// ReplaceHistory, when true, tells the server to treat the
	// provided History as the new authoritative transcript for this
	// session — any previously persisted messages beyond that length
	// are dropped before the new Message is appended. Used by the
	// "edit and resend" flow so an edit actually truncates future
	// turns instead of leaving stale context in the session.
	ReplaceHistory bool `json:"replace_history"`
}

// (chatResponse / toolCall types removed — chat now streams via SSE
// with discrete event types: chunk, tool_call, tool_result, done, error.)

// activeChats serializes per-IP requests so the same user can't have two
// in flight at once. Cheap concurrency limit for a testing tool.
var (
	activeChatsMu sync.Mutex
	activeChats   = make(map[string]bool)
)

func (T *ChatAgent) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	// Per-IP single-flight guard.
	clientKey := r.RemoteAddr
	activeChatsMu.Lock()
	if activeChats[clientKey] {
		activeChatsMu.Unlock()
		http.Error(w, "another request is in progress", http.StatusTooManyRequests)
		return
	}
	activeChats[clientKey] = true
	activeChatsMu.Unlock()
	defer func() {
		activeChatsMu.Lock()
		delete(activeChats, clientKey)
		activeChatsMu.Unlock()
	}()

	// Resolve the backing session. If the client provided a SessionID,
	// load it; otherwise mint a fresh one. Ownership check: a user can
	// only post into a session they own. New sessions are stamped with
	// the caller's username so subsequent requests and the sessions
	// listing endpoint scope correctly.
	username := sessionUsername(r)
	var session ChatSession
	isNewSession := false
	if req.SessionID != "" {
		if s, ok := LoadChatSession(T.DB, req.SessionID); ok && s.Username == username {
			session = s
		} else {
			// Provided ID is unknown or not owned — treat as new.
			isNewSession = true
		}
	} else {
		isNewSession = true
	}
	if isNewSession {
		session = ChatSession{
			ID:       UUIDv4(),
			Username: username,
			Created:  time.Now(),
			LastAt:   time.Now(),
		}
	}

	// Build the message history for the agent loop. Prefer the
	// session's persisted messages over the client-sent history so the
	// server is the source of truth. When the session is empty (new),
	// fall back to the client's History field so the transition from
	// the old single-session client works without changes.
	// ReplaceHistory overrides: the client is editing a past turn and
	// explicitly wants the session truncated to the supplied history
	// before this new Message is appended.
	if req.ReplaceHistory {
		trimmed := make([]ChatMessage, 0, len(req.History))
		for _, m := range req.History {
			trimmed = append(trimmed, ChatMessage{Role: m.Role, Content: m.Content})
		}
		session.Messages = trimmed
	}
	messages := make([]Message, 0, len(session.Messages)+1)
	if len(session.Messages) > 0 {
		for _, m := range session.Messages {
			messages = append(messages, Message{Role: m.Role, Content: m.Content})
		}
	} else {
		for _, m := range req.History {
			messages = append(messages, Message{Role: m.Role, Content: m.Content})
		}
	}
	messages = append(messages, Message{Role: "user", Content: req.Message})

	// Resolve tools. The chat app enforces its own blocklist regardless of
	// what the client requests, so a malicious or curious user can't pull
	// a blocked tool into the chat by name.
	var toolNames []string
	if req.PrivateMode {
		for _, t := range FilterChatToolsPrivate() {
			toolNames = append(toolNames, t.Name())
		}
	} else if len(req.Tools) > 0 {
		for _, name := range req.Tools {
			if !BlockedTools[name] {
				toolNames = append(toolNames, name)
			}
		}
	} else {
		for _, t := range allowedTools() {
			toolNames = append(toolNames, t.Name())
		}
	}

	agent := &AppCore{LLM: T.AppCore.LLM, LeadLLM: T.AppCore.LeadLLM, MaxRounds: 8, PromptTools: T.AppCore.PromptTools}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Build a per-user tool session with a sandbox workspace so file/exec
	// tools (read_file, list_directory, write_file, run_local) can resolve
	// paths against `<data>/workspaces/<username>/`. Failure to provision
	// the dir is logged but not fatal — the sandboxed tools just return
	// "no session WorkspaceDir" errors and other tools keep working.
	sess := &ToolSession{LLM: agent.LLM, LeadLLM: agent.LeadLLM}
	if ws, err := EnsureWorkspaceDir(sessionUsername(r)); err == nil {
		sess.WorkspaceDir = ws
	} else {
		Debug("[chat] workspace setup failed for %s: %v — sandboxed tools disabled", sessionUsername(r), err)
	}

	tools, terr := GetAgentToolsWithSession(sess, toolNames...)
	if terr != nil {
		writeSSEEvent(w, "error", map[string]string{"message": "tool resolve failed: " + terr.Error()})
		return
	}

	// Capability gating: by default chat permits CapRead + CapNetwork only.
	// Tools that declare CapWrite or CapExecute (write_file, run_local,
	// send_email, etc.) are filtered out before the LLM ever sees the
	// catalog, so even a hallucinated call by name can't reach the handler.
	// Opting in to higher tiers per session is future work; for now this is
	// a hard, always-on baseline.
	tools = FilterToolsByCaps(tools, []Capability{CapRead, CapNetwork})

	// Set up SSE headers — single open response, server pushes events as
	// the agent loop progresses (chunks, tool calls, tool results, done).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Wire send_status: when a tool fires sess.StatusCallback, emit an
	// SSE 'status' event so the client can render an inline progress
	// note. The LLM's final reply still flows through the normal chunk
	// path. Set after flusher is resolved so the closure can flush.
	sess.StatusCallback = func(text string) {
		writeSSEEvent(w, "status", map[string]string{"text": text})
		flusher.Flush()
	}

	// flushNewImages emits SSE `image` events for any new entries the
	// tool calls have appended to sess.Images since the last flush. Called
	// after each tool round and at session close so the user sees images
	// (screenshots, fetched/generated images) inline alongside text.
	// Tool handlers run synchronously in this goroutine, so a direct read
	// of sess.Images is race-free here.
	imagesFlushed := 0
	flushNewImages := func() {
		if sess == nil {
			return
		}
		for i := imagesFlushed; i < len(sess.Images); i++ {
			writeSSEEvent(w, "image", map[string]string{"data": sess.Images[i]})
			flusher.Flush()
		}
		imagesFlushed = len(sess.Images)
	}

	// Announce the session ID to the client up front so it can update
	// its currentSessionId before the response finishes — this way the
	// sidebar can refresh as soon as a title appears.
	writeSSEEvent(w, "session", map[string]string{"id": session.ID})
	flusher.Flush()

	// Accumulator that mirrors what the user sees streamed, so we can
	// persist a faithful transcript without re-reading the raw
	// resp.Content (which in prompt-tools mode still contains
	// <tool_call> and procedure tags before they're stripped). Each
	// round's emitChunk appends to this.
	var assistantReply strings.Builder

	// Persist the exchange and kick off auto-titling once the handler
	// returns — defer fires regardless of which return path (done,
	// error, canceled) we take, so every completed or interrupted
	// turn gets saved.
	defer func() {
		session.Messages = append(session.Messages, ChatMessage{Role: "user", Content: req.Message})
		if reply := strings.TrimSpace(assistantReply.String()); reply != "" {
			session.Messages = append(session.Messages, ChatMessage{Role: "assistant", Content: reply})
		}
		session.LastAt = time.Now()
		needTitle := isNewSession && session.Title == "" && len(session.Messages) >= 2
		SaveChatSession(T.DB, session)
		if needTitle {
			// Generate the title asynchronously — the HTTP response is
			// already flushed and the user is back to the UI. The title
			// lands on a subsequent sidebar poll.
			go func(s ChatSession) {
				titleCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				titleAgent := &AppCore{LLM: T.AppCore.LLM}
				worker := titleAgent.CreateSession(WORKER)
				title := GenerateSessionTitle(titleCtx, worker, s)
				if title == "" {
					return
				}
				// Re-load before saving so we don't clobber any newer
				// messages that landed while the title call was in
				// flight. Only set the title if it's still empty —
				// avoids overwriting a user-edited title in the future.
				latest, ok := LoadChatSession(T.DB, s.ID)
				if !ok || latest.Title != "" {
					return
				}
				latest.Title = title
				SaveChatSession(T.DB, latest)
			}(session)
		}
	}()

	// Streaming agent loop. Mirrors RunAgentLoop's structure but uses
	// ChatStream so we can push tokens to the client as they arrive, and
	// emits SSE events at each phase (chunks, tool calls, tool results).
	streamMessages := make([]Message, len(messages))
	copy(streamMessages, messages)

	maxRounds := 8
	// Prefix with today's date so the model can reason about current
	// events without defaulting to its training-cutoff knowledge.
	// Without this, a question like "who is the current president?"
	// returns whoever was president when the model was last trained.
	today := time.Now().Format("Monday, January 2, 2006")
	systemPrompt := fmt.Sprintf("Today is %s.\n\n", today) + T.SystemPrompt() + buildProcedurePrompt(T.DB)
	promptTools := agent.PromptTools

	toolDefs := make([]Tool, 0, len(tools))
	handlers := make(map[string]ToolHandlerFunc)
	for _, td := range tools {
		toolDefs = append(toolDefs, td.Tool)
		handlers[td.Tool.Name] = td.Handler
	}

	// In PromptTools mode, inject tool descriptions into the system prompt.
	if promptTools && len(tools) > 0 {
		systemPrompt += BuildToolPrompt(tools)
	}

	for round := 1; round <= maxRounds; round++ {
		opts := []ChatOption{}
		if systemPrompt != "" {
			opts = append(opts, WithSystemPrompt(systemPrompt))
		}
		// Only offer native tools when NOT in PromptTools mode.
		if !promptTools && len(toolDefs) > 0 && round < maxRounds {
			opts = append(opts, WithTools(toolDefs))
		}
		opts = append(opts, WithMaxTokens(2048), WithThink(false))

		// Stream this round's response. In PromptTools mode, stream chunks
		// to the client but hold back a trailing buffer that could be the
		// start of a <tool_call> tag. Once we're sure the trailing text is
		// NOT a tag prefix, flush it. If a full tag appears, stop streaming
		// and let the post-response handler deal with it.
		// Control tags that should be suppressed from the stream.
		// All start with "<" so we hold back content once we see "<".
		controlTags := []string{"<tool_call>", "<save_procedure>", "<delete_procedure>"}

		var promptBuf strings.Builder
		var holdback string       // trailing chars that might be a control tag
		var pendingNewlines int   // deferred trailing newlines — only emitted when more text follows
		tagDetected := false      // true once a control tag is found

		// emitChunk sends text to the client but defers trailing newlines.
		// They only get emitted when more non-whitespace text follows,
		// so the response never ends with blank lines. Also appends to
		// assistantReply so the persisted session transcript mirrors
		// what the user saw streamed.
		emitChunk := func(text string) {
			if text == "" {
				return
			}
			// Strip trailing newlines and defer them.
			trimmed := strings.TrimRight(text, "\n\r")
			trailingCount := len(text) - len(trimmed)

			// If we have deferred newlines and new non-empty content, emit them first.
			if pendingNewlines > 0 && trimmed != "" {
				nl := strings.Repeat("\n", pendingNewlines)
				writeSSEEvent(w, "chunk", map[string]string{"text": nl})
				flusher.Flush()
				assistantReply.WriteString(nl)
				pendingNewlines = 0
			}

			if trimmed != "" {
				writeSSEEvent(w, "chunk", map[string]string{"text": trimmed})
				flusher.Flush()
				assistantReply.WriteString(trimmed)
			}
			pendingNewlines += trailingCount
		}

		// Route to lead or worker based on routing config. Apply thinking override.
		chatLLM := agent.LLM
		if RouteToLead("chat.respond") {
			chatLLM = agent.GetLeadLLM()
		} else if think := RouteThink("chat.respond"); think != nil {
			opts = append(opts, WithThink(*think))
		}
		resp, err := chatLLM.ChatStream(ctx, streamMessages, func(chunk string) {
			if chunk == "" {
				return
			}
			if !promptTools {
				emitChunk(chunk)
				return
			}

			promptBuf.WriteString(chunk)

			if tagDetected {
				return // inside a control tag, suppress everything
			}

			holdback += chunk

			// Check if any control tag appeared in the holdback.
			for _, tag := range controlTags {
				if idx := strings.Index(holdback, tag); idx >= 0 {
					tagDetected = true
					if idx > 0 {
						emitChunk(holdback[:idx])
					}
					holdback = ""
					return
				}
			}

			// If holdback contains a "<", hold everything from the "<"
			// onward — it might be the start of a control tag.
			if idx := strings.LastIndex(holdback, "<"); idx >= 0 {
				safe := holdback[:idx]
				if safe != "" {
					emitChunk(safe)
				}
				holdback = holdback[idx:]
				return
			}

			// No "<" anywhere — safe to flush everything.
			emitChunk(holdback)
			holdback = ""
		}, opts...)
		if err != nil {
			writeSSEEvent(w, "error", map[string]string{"message": err.Error()})
			flusher.Flush()
			return
		}

		// PromptTools path: parse <tool_call> tags from the buffered text.
		// Emit only the preamble (text before the tag) to the client.
		if promptTools {
			if resp == nil {
				flushNewImages()
				writeSSEEvent(w, "done", map[string]any{"round": round})
				flusher.Flush()
				return
			}

			tc, preamble := ParsePromptToolCall(resp.Content, handlers)
			if tc == nil {
				// No tool call — parse procedure tags from the full buffer,
				// then flush any remaining holdback (stripped of procedure tags).
				parseProcedureActions(T.DB, promptBuf.String())
				remaining := strings.TrimRight(parseProcedureActions(nil, holdback), "\n\r ") // strip tags + trailing whitespace
				if remaining != "" {
					writeSSEEvent(w, "chunk", map[string]string{"text": remaining})
					flusher.Flush()
				}
				flushNewImages()
				writeSSEEvent(w, "done", map[string]any{"round": round})
				flusher.Flush()
				return
			}

			// Emit only the preamble (text before <tool_call>) to the client.
			// Do NOT add preamble to message history — the LLM will repeat it
			// on the next round if it sees its own preamble as a prior message.
			if preamble = strings.TrimRight(preamble, "\n\r "); preamble != "" {
				emitChunk(preamble)
			}

			// Execute the tool and send SSE events.
			args_json, _ := json.Marshal(tc.Args)
			writeSSEEvent(w, "tool_call", map[string]string{
				"name": tc.Name,
				"args": string(args_json),
			})
			flusher.Flush()

			output, toolErr := handlers[tc.Name](tc.Args)
			var resultText string
			if toolErr != nil {
				resultText = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, toolErr)
			} else {
				resultText = fmt.Sprintf("Tool result from %s:\n%s", tc.Name, output)
			}
			writeSSEEvent(w, "tool_result", map[string]string{
				"name":   tc.Name,
				"result": truncate(resultText, 2000),
			})
			flusher.Flush()

			// Send result back as a plain user message.
			streamMessages = append(streamMessages, Message{Role: "user", Content: resultText})
			flushNewImages()
			continue
		}

		// Native tool path (existing behavior).

		// No tool calls → this is the final answer. Send done and exit.
		if resp == nil || len(resp.ToolCalls) == 0 {
			// Parse procedure saves/deletes from the streamed response.
			if resp != nil && resp.Content != "" {
				parseProcedureActions(T.DB, resp.Content)
			}
			flushNewImages()
			writeSSEEvent(w, "done", map[string]any{"round": round})
			flusher.Flush()
			return
		}

		// Tool calls present. Append the assistant's tool-call message to
		// history, then run each tool and append the result via the next
		// message's ToolResults field (matches the framework's Message shape).
		assistantMsg := Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		streamMessages = append(streamMessages, assistantMsg)

		var results []ToolResult
		for _, tc := range resp.ToolCalls {
			args_json, _ := json.Marshal(tc.Args)
			writeSSEEvent(w, "tool_call", map[string]string{
				"name": tc.Name,
				"args": string(args_json),
			})
			flusher.Flush()

			handler, ok := handlers[tc.Name]
			var output string
			var toolErr error
			if !ok {
				toolErr = fmt.Errorf("unknown tool: %s", tc.Name)
			} else {
				output, toolErr = handler(tc.Args)
			}

			result := output
			isErr := toolErr != nil
			if isErr {
				result = "ERROR: " + toolErr.Error()
			}
			writeSSEEvent(w, "tool_result", map[string]string{
				"name":   tc.Name,
				"result": truncate(result, 2000),
			})
			flusher.Flush()

			results = append(results, ToolResult{
				ID:      tc.ID,
				Content: result,
				IsError: isErr,
			})
		}
		// Tool results go in a "user" role message with ToolResults set —
		// this matches the format the framework's RunAgentLoop uses (see
		// core/agent_loop.go) and is what buildMessages knows how to
		// translate to native ollama tool-response messages.
		streamMessages = append(streamMessages, Message{
			Role:        "user",
			ToolResults: results,
		})
		// Drain any view-images a tool deposited (e.g. view_video /
		// download_video sampling frames for visual analysis). These go
		// into a follow-up user message with Images attached so the LLM
		// sees them on the next round. They are NOT pushed to sess.Images
		// so the chat UI doesn't render them as outbound attachments.
		if viewImgs := sess.DrainViewImages(); len(viewImgs) > 0 {
			streamMessages = append(streamMessages, Message{
				Role:    "user",
				Content: "Here are the sampled frames for visual analysis:",
				Images:  viewImgs,
			})
		}
		flushNewImages()
	}

	// Hit the max-rounds cap without a final answer.
	writeSSEEvent(w, "error", map[string]string{"message": fmt.Sprintf("agent loop exceeded %d rounds", maxRounds)})
	flusher.Flush()
}

// writeSSEEvent writes a single Server-Sent Event with a name and JSON payload.
func writeSSEEvent(w http.ResponseWriter, eventType string, data any) {
	body, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(body))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... [truncated, %d chars total]", len(s))
}

// --- HTML / CSS / JS ---

const chatCSS = `
body { margin: 0; height: 100vh; height: 100dvh; overflow: hidden; }
#chat-layout { display: flex; flex-direction: row; height: 100%; width: 100%; }
#chat-main { display: flex; flex-direction: column; flex: 1; min-width: 0; height: 100%; }

/* Sidebar: open-webui style session list pinned to the left. Fixed
   width on desktop, slides off-canvas on mobile behind a hamburger. */
#chat-sidebar {
  width: 260px; flex-shrink: 0;
  display: flex; flex-direction: column;
  background: var(--bg-1); border-right: 1px solid var(--border);
  overflow: hidden;
}
#chat-sidebar.hidden { display: none; }
#sidebar-head {
  display: flex; gap: 0.4rem; align-items: center;
  padding: 0.6rem 0.75rem;
  border-bottom: 1px solid var(--border);
}
#sidebar-new { flex: 1; padding: 0.45rem 0.75rem; font-size: 0.85rem; }
#sidebar-close {
  background: transparent; border: none; color: var(--text-mute);
  cursor: pointer; font-size: 1rem; padding: 0.2rem 0.45rem; line-height: 1;
}
#sidebar-close:hover { color: var(--text-hi); }
#sidebar-filter {
  padding: 0.35rem 0.6rem; border-bottom: 1px solid var(--border);
}
#sidebar-archived-toggle {
  background: transparent; border: none; color: var(--text-mute);
  cursor: pointer; font-size: 0.75rem; padding: 0.2rem 0.3rem;
  border-radius: 3px;
}
#sidebar-archived-toggle:hover { color: var(--text-hi); background: var(--bg-2); }
#sidebar-archived-toggle.active { color: var(--accent); }
#sidebar-select-toggle {
  background: transparent; border: 1px solid var(--border); color: var(--text-mute);
  font-size: 0.75rem; padding: 0.25rem 0.6rem; border-radius: 4px;
  cursor: pointer; transition: color 0.15s, background 0.15s, border-color 0.15s;
}
#sidebar-select-toggle:hover { color: var(--text-hi); background: var(--bg-2); }
#sidebar-select-toggle.active { color: var(--accent); border-color: var(--accent); }
#sidebar-bulkbar {
  display: flex; gap: 0.4rem; align-items: center; flex-wrap: wrap;
  padding: 0.4rem 0.75rem; background: var(--bg-2);
  border-bottom: 1px solid var(--border); font-size: 0.78rem;
}
#bulkbar-count { color: var(--text-mute); margin-right: auto; }
#sidebar-bulkbar button {
  background: transparent; border: 1px solid var(--border); color: var(--text);
  font-size: 0.75rem; padding: 0.2rem 0.55rem; border-radius: 4px; cursor: pointer;
}
#sidebar-bulkbar button:hover { background: var(--bg-1); }
#sidebar-bulkbar button.danger { color: #f85149; border-color: #f85149; }
#sidebar-bulkbar button.danger:hover { background: rgba(248, 81, 73, 0.12); }
#sidebar-bulkbar button:disabled { opacity: 0.4; cursor: not-allowed; }
.session-item .si-check {
  display: none; margin-right: 0.4rem; cursor: pointer;
  width: 14px; height: 14px;
}
body.select-mode .session-item .si-check { display: inline-block; }
body.select-mode .session-item .si-actions { display: none; }
.session-item.selected { background: rgba(88, 166, 255, 0.18); }
#sessions-list {
  flex: 1; overflow-y: auto;
  padding: 0.4rem 0.4rem 0.75rem;
}
.session-item {
  display: flex; align-items: center; gap: 0.4rem;
  padding: 0.45rem 0.55rem;
  border-radius: 6px;
  cursor: pointer;
  color: var(--text);
  font-size: 0.85rem;
  line-height: 1.3;
  position: relative;
}
.session-item:hover { background: var(--bg-2); }
.session-item.active { background: var(--bg-2); color: var(--text-hi); }
.session-item .si-title {
  flex: 1; min-width: 0;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.session-item[data-archived="true"] .si-title { color: var(--text-mute); font-style: italic; }
.session-item .si-actions {
  display: none; gap: 0.15rem; flex-shrink: 0;
}
.session-item:hover .si-actions,
.session-item.active .si-actions { display: flex; }
.session-item .si-actions button {
  background: transparent; border: none; color: var(--text-mute);
  cursor: pointer; padding: 0.15rem 0.3rem; font-size: 0.8rem;
  border-radius: 3px;
}
.session-item .si-actions button:hover { color: var(--text-hi); background: var(--bg-0); }
#sessions-empty {
  color: var(--text-mute); font-size: 0.8rem;
  padding: 1rem 0.75rem; text-align: center;
}
#sidebar-toggle {
  background: transparent; border: none; color: var(--text-mute);
  cursor: pointer; font-size: 1.1rem; padding: 0.2rem 0.45rem; line-height: 1;
  display: none;
}
#sidebar-toggle:hover { color: var(--text-hi); }
#chat-layout.sidebar-hidden #sidebar-toggle { display: inline-block; }
@media (min-width: 601px) {
  #chat-layout.sidebar-hidden #chat-sidebar { display: none; }
}

#chat-header { padding: 0.75rem 1rem; background: var(--bg-1); border-bottom: 1px solid var(--border); display: flex; align-items: center; gap: 0.75rem; }
#chat-header h1 { font-size: 1rem; margin: 0; color: var(--text-hi); }
#tools-summary { color: var(--text-mute); font-size: 0.85rem; margin-left: auto; cursor: pointer; }
#tools-summary:hover { color: var(--text-hi); }
#private-toggle {
  background: transparent; border: 1px solid var(--border); color: var(--text-mute);
  font-size: 0.75rem; padding: 0.2rem 0.5rem; border-radius: 4px; cursor: pointer;
  white-space: nowrap;
}
#private-toggle:hover { color: var(--text-hi); border-color: var(--text-mute); }
#private-toggle.active { color: var(--accent); border-color: var(--accent); }
#tools-list { display: none; padding: 0.5rem 1rem; background: var(--bg-2); border-bottom: 1px solid var(--border); font-size: 0.8rem; color: var(--text-mute); max-height: 200px; overflow-y: auto; }
#tools-list .tool { padding: 0.2rem 0; }
#tools-list .tool b { color: var(--text); margin-right: 0.5rem; }
#chat-history {
  flex: 1; overflow-y: auto;
  padding: 0.75rem 1rem;
  max-width: 760px; margin: 0 auto; width: 100%;
}
.chat-msg {
  margin-bottom: 0.5rem; line-height: 1.5;
}
.chat-msg.user {
  display: flex; flex-direction: column; align-items: flex-end;
  margin-left: auto; max-width: fit-content;
  background: transparent; color: inherit; padding: 0; border-radius: 0;
}
.chat-msg.user .bubble {
  background: transparent; color: var(--text);
  padding: 0.25rem 0.55rem; border-radius: 10px;
  border: 1px solid var(--border);
  line-height: 1.35; font-size: 0.9rem;
  max-width: 100%;
}
.chat-msg.user .bubble pre { color: var(--text); }
.chat-msg.user .edit-area {
  display: flex; flex-direction: column; gap: 0.3rem;
  width: min(640px, 90vw);
}
.chat-msg.user .edit-area textarea {
  background: var(--bg-0); border: 1px solid var(--accent); color: var(--text);
  border-radius: 6px; padding: 0.4rem 0.6rem; font: inherit;
  font-size: 0.9rem; min-height: 60px; resize: vertical;
}
.chat-msg.user .edit-area .buttons { display: flex; gap: 0.35rem; justify-content: flex-end; }
.chat-msg.user .edit-area button {
  padding: 0.25rem 0.7rem; font-size: 0.8rem; border-radius: 4px;
  border: 1px solid var(--border); background: var(--bg-1); color: var(--text); cursor: pointer;
}
.chat-msg.user .edit-area button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
.chat-msg.user .msg-actions { justify-content: flex-end; }
/* Assistant messages flow the full column width with no bubble or
   border — open-webui style. A small muted "Gohort" label sits above
   each assistant turn so whose-turn-is-whose reads at a glance. */
.chat-msg.assistant {
  background: transparent; border: none; padding: 0.1rem 0;
  position: relative;
}
.chat-msg.assistant::before {
  content: "Gohort";
  display: block;
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.05em;
  color: var(--text-mute, #8b949e);
  text-transform: uppercase;
  margin-bottom: 0.25rem;
}
.chat-msg.error {
  background: transparent; border-left: 2px solid var(--danger);
  padding: 0.2rem 0.6rem; color: #ffb4b4;
}
.chat-msg pre { white-space: pre-wrap; word-break: break-word; margin: 0; font-family: inherit; }
/* Thinking indicator: three pulsing dots shown in the assistant
   bubble between the user hitting send and the first chunk (or
   tool_call event) arriving. Removed on first content so the dots
   don't overlap streamed text. */
.chat-msg .thinking-dots {
  display: inline-flex; gap: 0.3rem; padding: 0.15rem 0;
}
.chat-msg .thinking-dots span {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--text-mute, #8b949e);
  animation: chat-thinking 1.4s infinite ease-in-out both;
}
.chat-msg .thinking-dots span:nth-child(2) { animation-delay: 0.2s; }
.chat-msg .thinking-dots span:nth-child(3) { animation-delay: 0.4s; }
@keyframes chat-thinking {
  0%, 80%, 100% { opacity: 0.3; transform: scale(0.75); }
  40% { opacity: 1; transform: scale(1); }
}
/* Rendered-markdown variant: replaces the streaming <pre> once the
   assistant's response completes. Sized to read as flowing prose, not
   the oversized defaults h1/h2 would otherwise inherit. */
.chat-msg .content.md { line-height: 1.55; }
.chat-msg .content.md p { margin: 0 0 0.5rem; }
.chat-msg .content.md p:last-child { margin-bottom: 0; }
.chat-msg .content.md h1,
.chat-msg .content.md h2,
.chat-msg .content.md h3,
.chat-msg .content.md h4,
.chat-msg .content.md h5 { margin: 0.7rem 0 0.3rem; font-weight: 600; line-height: 1.3; }
.chat-msg .content.md h1 { font-size: 1.1rem; }
.chat-msg .content.md h2 { font-size: 1rem; }
.chat-msg .content.md h3 { font-size: 0.95rem; color: var(--text-mute); }
.chat-msg .content.md h4,
.chat-msg .content.md h5 { font-size: 0.9rem; color: var(--text-mute); }
.chat-msg .content.md ul,
.chat-msg .content.md ol { margin: 0.25rem 0 0.5rem; padding-left: 1.4rem; }
.chat-msg .content.md li { margin: 0.1rem 0; }
.chat-msg .content.md a { color: var(--accent); text-decoration: none; }
.chat-msg .content.md a:hover { text-decoration: underline; }
.chat-msg .content.md strong { color: var(--text); }
.chat-msg .content.md code { background: var(--bg-2); padding: 0.05rem 0.3rem; border-radius: 3px; font-family: ui-monospace, Menlo, monospace; font-size: 0.85em; }
.tool-call { margin-top: 0.4rem; background: var(--bg-2); border-left: 3px solid var(--warn); border-radius: 4px; font-size: 0.8rem; color: var(--text-mute); }
.tool-call summary { padding: 0.4rem 0.6rem; cursor: pointer; list-style: none; display: flex; align-items: center; gap: 0.4rem; }
.tool-call summary::-webkit-details-marker { display: none; }
.tool-call summary::before { content: '▶'; font-size: 0.6rem; color: var(--text-mute); transition: transform 0.15s; }
.tool-call[open] summary::before { transform: rotate(90deg); }
.tool-call .name { color: var(--warn); font-weight: 600; }
.tool-call .tool-status { color: var(--text-mute); font-style: italic; font-size: 0.75rem; }
.tool-call.pending .tool-status { color: var(--text-mute); }
.tool-call:not(.pending) .tool-status { color: var(--green, #3fb950); font-style: normal; }
.tool-details { padding: 0.3rem 0.6rem 0.4rem; }
.tool-call .args, .tool-call .result { display: block; margin-top: 0.2rem; font-family: ui-monospace, Menlo, monospace; font-size: 0.75rem; white-space: pre-wrap; word-break: break-word; }
.tool-call .result { color: var(--text); }
/* In-progress status messages from the send_status tool. Rendered as a
   subdued italic note inline in the assistant bubble so the user knows
   work is still happening. */
.status-note { margin-top: 0.4rem; padding: 0.3rem 0.6rem; background: var(--bg-2); border-left: 3px solid var(--accent, #4f8cff); border-radius: 4px; font-size: 0.8rem; font-style: italic; color: var(--text-mute); }
.status-note::before { content: '… '; font-style: normal; }
/* Inline images delivered alongside the assistant's reply: screenshots,
   fetched images, generated images. Click opens at full size. */
.tool-images { display: flex; flex-direction: column; gap: 0.5rem; margin-top: 0.6rem; }
.tool-image { max-width: 100%; max-height: 480px; border-radius: 6px; border: 1px solid var(--border); cursor: zoom-in; background: var(--bg-2); }
.tool-image:hover { border-color: var(--accent); }
/* Action row shown under a completed assistant message: copy/retry
   buttons, open-webui style. Fades in on hover to stay out of the way
   while reading, and is always visible on touch devices. */
.msg-actions {
  display: flex; gap: 0.25rem; margin-top: 0.3rem;
  opacity: 0; transition: opacity 0.15s ease;
}
.chat-msg:hover .msg-actions,
.chat-msg:focus-within .msg-actions { opacity: 1; }
.msg-actions button {
  background: transparent; border: 1px solid transparent;
  color: var(--text-mute); cursor: pointer;
  padding: 0.2rem 0.45rem; border-radius: 4px;
  font-size: 0.75rem; line-height: 1;
}
.msg-actions button:hover { color: var(--text-hi); border-color: var(--border); background: var(--bg-2); }
.msg-actions button.copied { color: var(--green, #3fb950); }
@media (hover: none) { .msg-actions { opacity: 1; } }
.chat-msg.error { background: #2d1a1a; border: 1px solid var(--danger); color: #ffb4b4; }
#chat-input-area {
  display: flex; gap: 0.5rem;
  padding: 0.75rem 1rem 1rem;
  max-width: 760px; width: 100%; margin: 0 auto;
  background: transparent;
}
#chat-input { flex: 1; min-height: 38px; max-height: 200px; padding: 0.5rem 0.75rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 6px; color: var(--text); font-family: inherit; font-size: 0.9rem; resize: vertical; }
#chat-input:focus { border-color: var(--accent); outline: none; }
#chat-send { padding: 0 1.25rem; }
#chat-send:disabled { opacity: 0.5; cursor: not-allowed; }
#chat-attach {
  padding: 0 0.75rem; font-size: 1.1rem; background: var(--bg-0);
  color: var(--text-mute); border: 1px solid var(--border); border-radius: 6px;
  cursor: pointer;
}
#chat-attach:hover { color: var(--text-hi); border-color: var(--accent); }

/* Mobile responsive */
@media (max-width: 600px) {
  /* Sidebar: off-canvas drawer. Open state slides it in; hamburger
     in the header toggles it. Overlays chat-main rather than
     resizing so small screens keep the full chat width. */
  #chat-sidebar {
    position: fixed; top: 0; bottom: 0; left: 0;
    z-index: 20; width: 80%; max-width: 280px;
    box-shadow: 2px 0 12px rgba(0,0,0,0.3);
    transform: translateX(-100%); transition: transform 0.2s ease;
  }
  #chat-layout:not(.sidebar-hidden) #chat-sidebar { transform: translateX(0); }
  #chat-layout.sidebar-hidden #chat-sidebar { transform: translateX(-100%); }
  /* On mobile, the sidebar starts hidden — unlike desktop. The
     toggle button is shown whenever the sidebar isn't open. */
  #sidebar-toggle { display: inline-block; }
  #chat-layout:not(.sidebar-hidden) #sidebar-toggle { display: none; }

  #chat-header { padding: 0.5rem; gap: 0.5rem; flex-wrap: wrap; }
  #chat-header h1 { font-size: 0.9rem; }
  #tools-summary { font-size: 0.75rem; }
  #chat-history { padding: 0.5rem; }
  .chat-msg { max-width: 95%; font-size: 0.9rem; padding: 0.5rem 0.7rem; }
  .chat-msg.user { max-width: 85%; }
  .chat-msg pre { font-size: 0.8rem; }
  .tool-call { font-size: 0.75rem; }
  .tool-call .args, .tool-call .result { font-size: 0.7rem; }
  #chat-input-area { padding: 0.5rem; padding-bottom: calc(0.5rem + env(safe-area-inset-bottom, 0px)); gap: 0.4rem; }
  #chat-input { font-size: 1rem; min-height: 44px; padding: 0.6rem; -webkit-appearance: none; }
  #chat-send { padding: 0 1rem; min-height: 44px; font-size: 0.9rem; }
  #tools-list { font-size: 0.75rem; }
}
`

const chatBody = `
<div id="chat-layout">
  <aside id="chat-sidebar">
    <div id="sidebar-head">
      <button id="sidebar-new" class="primary" onclick="newChat()">+ New Chat</button>
      <button id="sidebar-close" onclick="toggleSidebar(false)" title="Hide sidebar" aria-label="Hide sidebar">✕</button>
    </div>
    <div id="sidebar-filter">
      <button id="sidebar-archived-toggle" type="button" onclick="toggleArchivedSessions()">Show archived</button>
      <button id="sidebar-select-toggle" type="button" onclick="toggleSelectMode()">Select</button>
    </div>
    <div id="sidebar-bulkbar" style="display:none">
      <span id="bulkbar-count">0 selected</span>
      <button id="bulkbar-all" type="button" onclick="bulkSelectAll()">All</button>
      <button id="bulkbar-archive" type="button" onclick="bulkArchive()">Archive</button>
      <button id="bulkbar-delete" type="button" onclick="bulkDelete()" class="danger">Delete</button>
      <button id="bulkbar-cancel" type="button" onclick="toggleSelectMode()">Done</button>
    </div>
    <div id="sessions-list"></div>
  </aside>
  <div id="chat-main">
    <div id="chat-header">
      <button id="sidebar-toggle" onclick="toggleSidebar(true)" title="Show sessions" aria-label="Show sessions">☰</button>
      <span class="app-title">Chat</span>
      <h1 id="chat-title" style="display:none">Chat — Tool Tester</h1>
      <button id="private-toggle" title="Toggle private mode (no internet tools)" onclick="togglePrivateMode()">Private</button>
      <span id="tools-summary" onclick="toggleTools()">Loading tools…</span>
    </div>
    <div id="tools-list"></div>
    <div id="chat-history"></div>
    <div id="chat-input-area">
      <input type="file" id="chat-attach-file" style="display:none" onchange="handleAttachFile(event)">
      <button id="chat-attach" title="Attach a text file (log, config, etc.)" onclick="document.getElementById('chat-attach-file').click()">📎</button>
      <textarea id="chat-input" placeholder="Message…" rows="1"></textarea>
      <button id="chat-send" class="primary" onclick="sendChat()">Send</button>
    </div>
  </div>
</div>
`

const chatJS = `
var chatHistory = [];
var sending = false;
// currentSessionId: the active persisted session on the server. Null
// means "next send starts a fresh one" — the server mints the ID and
// returns it via the first SSE event, which we stash here.
var currentSessionId = null;
var sessionsRefreshTimer = null;
var showArchivedSessions = false;

function escapeHtml(s) {
  return String(s == null ? '' : s)
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// ---------- Sidebar / sessions ----------

function toggleSidebar(show) {
  var layout = document.getElementById('chat-layout');
  if (!layout) return;
  if (show === true) layout.classList.remove('sidebar-hidden');
  else if (show === false) layout.classList.add('sidebar-hidden');
  else layout.classList.toggle('sidebar-hidden');
}

function loadSessions() {
  return fetch('api/sessions').then(function(r){
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function(sessions){
    renderSessions(sessions || []);
  }).catch(function(){
    // Silent — sessions list is best-effort. Don't clobber the chat.
  });
}

function toggleArchivedSessions() {
  showArchivedSessions = !showArchivedSessions;
  var btn = document.getElementById('sidebar-archived-toggle');
  if (btn) {
    btn.textContent = showArchivedSessions ? 'Hide archived' : 'Show archived';
    btn.classList.toggle('active', showArchivedSessions);
  }
  loadSessions();
}

function renderSessions(sessions) {
  var list = document.getElementById('sessions-list');
  if (!list) return;
  var archivedCount = 0;
  var visible = [];
  for (var i = 0; i < sessions.length; i++) {
    var s = sessions[i];
    if (s.Archived) archivedCount++;
    if (!showArchivedSessions && s.Archived) continue;
    visible.push(s);
  }
  var toggleBtn = document.getElementById('sidebar-archived-toggle');
  if (toggleBtn) {
    // Hide the toggle entirely when there's nothing to show — avoids
    // advertising a mode that has no effect on a fresh account.
    toggleBtn.parentNode.style.display = archivedCount > 0 ? '' : 'none';
  }
  if (!visible.length) {
    list.innerHTML = '<div id="sessions-empty">No sessions yet. Send a message to start one.</div>';
    return;
  }
  var html = '';
  for (var j = 0; j < visible.length; j++) {
    var s = visible[j];
    var id = s.ID;
    var title = s.Title || s.Preview || 'New chat';
    var active = (id === currentSessionId) ? ' active' : '';
    var archAttr = s.Archived ? ' data-archived="true"' : '';
    var isSel = selectedSessions[id] ? ' selected' : '';
    var checked = selectedSessions[id] ? ' checked' : '';
    html += '<div class="session-item' + active + isSel + '" data-id="' + escapeHtml(id) + '"' + archAttr + '>'
      + '<input type="checkbox" class="si-check"' + checked + ' aria-label="Select chat">'
      + '<span class="si-title" title="' + escapeHtml(title) + '">' + escapeHtml(title) + '</span>'
      + '<span class="si-actions">'
      + '<button class="si-archive" title="' + (s.Archived ? 'Unarchive' : 'Archive') + '">' + (s.Archived ? '↺' : '🗄') + '</button>'
      + '<button class="si-delete" title="Delete">🗑</button>'
      + '</span>'
      + '</div>';
  }
  list.innerHTML = html;
  wireSessionItems();
  updateBulkBar();
}

// Multi-select state. selectMode tracks whether the sidebar is in
// checkbox-mode; selectedSessions is a {id: true} set so re-renders
// (which happen on every refresh) preserve selection state.
var selectMode = false;
var selectedSessions = {};

function toggleSelectMode() {
  selectMode = !selectMode;
  document.body.classList.toggle('select-mode', selectMode);
  var btn = document.getElementById('sidebar-select-toggle');
  if (btn) btn.classList.toggle('active', selectMode);
  var bar = document.getElementById('sidebar-bulkbar');
  if (bar) bar.style.display = selectMode ? 'flex' : 'none';
  if (!selectMode) selectedSessions = {};
  loadSessions();
}

function setSessionSelected(id, on) {
  if (on) selectedSessions[id] = true;
  else delete selectedSessions[id];
  var item = document.querySelector('.session-item[data-id="' + cssEscapeAttr(id) + '"]');
  if (item) item.classList.toggle('selected', !!on);
  updateBulkBar();
}

function cssEscapeAttr(s) {
  return String(s).replace(/["\\]/g, '\\$&');
}

function updateBulkBar() {
  var count = Object.keys(selectedSessions).length;
  var el = document.getElementById('bulkbar-count');
  if (el) el.textContent = count + (count === 1 ? ' selected' : ' selected');
  ['bulkbar-archive', 'bulkbar-delete'].forEach(function(bid) {
    var b = document.getElementById(bid);
    if (b) b.disabled = (count === 0);
  });
}

function bulkSelectAll() {
  var items = document.querySelectorAll('#sessions-list .session-item');
  // If everything visible is already selected, treat the click as "clear all".
  var allOn = items.length > 0;
  items.forEach(function(it) {
    var id = it.getAttribute('data-id');
    if (!selectedSessions[id]) { allOn = false; }
  });
  items.forEach(function(it) {
    var id = it.getAttribute('data-id');
    var cb = it.querySelector('.si-check');
    if (allOn) {
      delete selectedSessions[id];
      it.classList.remove('selected');
      if (cb) cb.checked = false;
    } else {
      selectedSessions[id] = true;
      it.classList.add('selected');
      if (cb) cb.checked = true;
    }
  });
  updateBulkBar();
}

function bulkArchive() {
  var ids = Object.keys(selectedSessions);
  if (!ids.length) return;
  // Archive endpoint is a toggle; document this in the confirm so users
  // know currently-archived sessions will unarchive.
  if (!confirm('Toggle archive on ' + ids.length + ' chat' + (ids.length === 1 ? '' : 's') + '?')) return;
  Promise.all(ids.map(function(id) {
    return fetch('api/sessions/archive/' + encodeURIComponent(id), {method: 'POST'});
  })).then(function() {
    selectedSessions = {};
    loadSessions();
  });
}

function bulkDelete() {
  var ids = Object.keys(selectedSessions);
  if (!ids.length) return;
  if (!confirm('Delete ' + ids.length + ' chat' + (ids.length === 1 ? '' : 's') + '? This cannot be undone.')) return;
  Promise.all(ids.map(function(id) {
    return fetch('api/sessions/delete/' + encodeURIComponent(id), {method: 'DELETE'});
  })).then(function() {
    // If the active session was in the deleted set, drop into a new chat.
    if (currentSessionId && selectedSessions[currentSessionId]) newChat();
    selectedSessions = {};
    loadSessions();
  });
}

function wireSessionItems() {
  var items = document.querySelectorAll('#sessions-list .session-item');
  for (var i = 0; i < items.length; i++) {
    (function(it) {
      var id = it.getAttribute('data-id');
      it.onclick = function(e) {
        // Select-mode: clicking the row toggles selection. Direct
        // checkbox clicks bubble up here too, so we read its state
        // post-click rather than flipping a stale value.
        if (selectMode) {
          if (e.target.closest('.si-actions')) return;
          var cb = it.querySelector('.si-check');
          if (e.target !== cb) {
            // Row click outside the checkbox — flip the checkbox value.
            if (cb) cb.checked = !cb.checked;
          }
          setSessionSelected(id, cb && cb.checked);
          return;
        }
        if (e.target.closest('.si-actions')) return;
        openSession(id);
      };
      var del = it.querySelector('.si-delete');
      if (del) del.onclick = function(e) {
        e.stopPropagation();
        if (!confirm('Delete this chat?')) return;
        fetch('api/sessions/delete/' + encodeURIComponent(id), {method: 'DELETE'})
          .then(function(){
            if (id === currentSessionId) newChat();
            loadSessions();
          });
      };
      var arch = it.querySelector('.si-archive');
      if (arch) arch.onclick = function(e) {
        e.stopPropagation();
        fetch('api/sessions/archive/' + encodeURIComponent(id), {method: 'POST'})
          .then(function(){ loadSessions(); });
      };
    })(items[i]);
  }
}

function openSession(id) {
  if (sending) return;
  fetch('api/sessions/' + encodeURIComponent(id)).then(function(r){
    if (!r.ok) throw new Error('HTTP ' + r.status);
    return r.json();
  }).then(function(s){
    currentSessionId = s.ID;
    chatHistory = [];
    var msgs = s.Messages || [];
    var hist = document.getElementById('chat-history');
    hist.innerHTML = '';
    for (var i = 0; i < msgs.length; i++) {
      var m = msgs[i];
      var role = m.role || m.Role;
      var content = m.content || m.Content || '';
      if (role === 'user') {
        appendUserMessage(content);
        chatHistory.push({role: 'user', content: content});
      } else if (role === 'assistant') {
        renderSavedAssistant(content);
        chatHistory.push({role: 'assistant', content: content});
      }
    }
    hist.scrollTop = hist.scrollHeight;
    loadSessions();
    // Collapse the sidebar on mobile after pick so the chat is visible.
    if (window.matchMedia && window.matchMedia('(max-width: 600px)').matches) {
      toggleSidebar(false);
    }
  }).catch(function(err){
    appendError(null, 'Failed to load session: ' + err.message);
  });
}

function renderSavedAssistant(text) {
  var hist = document.getElementById('chat-history');
  var div = document.createElement('div');
  div.className = 'chat-msg assistant';
  var content;
  if (typeof renderMarkdown === 'function' && text) {
    content = document.createElement('div');
    content.className = 'content md';
    content.innerHTML = renderMarkdown(text);
  } else {
    content = document.createElement('pre');
    content.className = 'content';
    content.textContent = text;
  }
  div.appendChild(content);
  var tools = document.createElement('div');
  tools.className = 'tools';
  div.appendChild(tools);
  addAssistantActions(div, text);
  hist.appendChild(div);
}

// addAssistantActions attaches the copy/retry button row to a
// completed assistant message. The raw text is stashed on the element
// so copy works regardless of how the content is rendered (markdown
// div vs. pre), and retry can find the preceding user turn.
function addAssistantActions(msgEl, rawText) {
  msgEl.dataset.raw = rawText || '';
  var bar = document.createElement('div');
  bar.className = 'msg-actions';
  bar.innerHTML =
    '<button type="button" class="act-copy" title="Copy response" onclick="copyAssistant(this)">Copy</button>' +
    '<button type="button" class="act-retry" title="Retry this response" onclick="retryAssistant(this)">Retry</button>';
  msgEl.appendChild(bar);
}

function copyAssistant(btn) {
  var msgEl = btn.closest('.chat-msg.assistant');
  if (!msgEl) return;
  var text = msgEl.dataset.raw || '';
  var done = function() {
    var orig = btn.textContent;
    btn.textContent = 'Copied';
    btn.classList.add('copied');
    setTimeout(function(){ btn.textContent = orig; btn.classList.remove('copied'); }, 1200);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(done, function(){});
  } else {
    var ta = document.createElement('textarea');
    ta.value = text; document.body.appendChild(ta);
    ta.select(); try { document.execCommand('copy'); done(); } catch(e) {}
    document.body.removeChild(ta);
  }
}

function retryAssistant(btn) {
  if (sending) return;
  var msgEl = btn.closest('.chat-msg.assistant');
  if (!msgEl) return;
  // The preceding sibling must be the user turn that produced this
  // assistant reply. If it isn't, bail rather than re-sending the
  // wrong message.
  var prev = msgEl.previousElementSibling;
  while (prev && !prev.classList.contains('chat-msg')) prev = prev.previousElementSibling;
  if (!prev || !prev.classList.contains('user')) return;
  var userPre = prev.querySelector('pre');
  var userText = userPre ? userPre.textContent : '';
  if (!userText) return;
  // Drop the in-memory history pair that corresponds to this exchange
  // so the backend doesn't see the prior (discarded) assistant reply
  // when we re-send.
  if (chatHistory.length && chatHistory[chatHistory.length - 1].role === 'assistant') chatHistory.pop();
  if (chatHistory.length && chatHistory[chatHistory.length - 1].role === 'user') chatHistory.pop();
  prev.remove();
  msgEl.remove();
  var input = document.getElementById('chat-input');
  input.value = userText;
  sendChat({replaceHistory: true});
}

function newChat() {
  if (sending) return;
  currentSessionId = null;
  chatHistory = [];
  var hist = document.getElementById('chat-history');
  if (hist) hist.innerHTML = '';
  // Deselect any currently-active item in the sidebar.
  var items = document.querySelectorAll('#sessions-list .session-item.active');
  for (var i = 0; i < items.length; i++) items[i].classList.remove('active');
  var input = document.getElementById('chat-input');
  if (input) input.focus();
  if (window.matchMedia && window.matchMedia('(max-width: 600px)').matches) {
    toggleSidebar(false);
  }
}

var privateMode = false;

// isGarbageText detects binary / non-printable garbage in LLM output.
// When the LLM returns non-text data (e.g. a crash dump, binary blob),
// the stream still delivers it as chunk events, so we need to validate
// the final text before rendering it as a successful response.
function isGarbageText(s) {
  if (!s) return true;
  // Null bytes → definitely binary.
  if (s.indexOf('\0') !== -1) return true;
  // Count non-printable control characters (excluding \n, \r, \t).
  var ctrl = 0, total = 0;
  for (var i = 0; i < s.length; i++) {
    var c = s.charCodeAt(i);
    if (c < 32 && c !== 10 && c !== 13 && c !== 9) { ctrl++; }
    else if (c > 127 && c < 160) { ctrl++; } // C1 control chars
    else if (c >= 32) { total++; } // printable
  }
  // If more than half of printable-char candidates are garbage, reject.
  if (total > 0 && ctrl > total * 2) return true;
  // If there are zero printable chars at all, it's all noise.
  if (total === 0) return true;
  return false;
}

function loadPrivateMode() {
  fetch('api/settings/private').then(function(r){return r.json()}).then(function(data){
    privateMode = data.private_mode;
    var btn = document.getElementById('private-toggle');
    btn.classList.toggle('active', privateMode);
    loadTools();
  });
}

function loadTools() {
  fetch('api/tools?private=' + privateMode).then(function(r){return r.json()}).then(function(tools){
    var summary = document.getElementById('tools-summary');
    summary.textContent = tools.length + ' tools available — click to expand';
    var list = document.getElementById('tools-list');
    var html = '';
    for (var i = 0; i < tools.length; i++) {
      html += '<div class="tool"><b>' + escapeHtml(tools[i].name) + '</b>' + escapeHtml(tools[i].desc) + '</div>';
    }
    list.innerHTML = html;
  });
}

function togglePrivateMode() {
  privateMode = !privateMode;
  fetch('api/settings/private/set', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({private_mode: privateMode})
  }).then(function(){
    var btn = document.getElementById('private-toggle');
    btn.classList.toggle('active', privateMode);
    loadTools();
  });
}

function toggleTools() {
  var list = document.getElementById('tools-list');
  list.style.display = list.style.display === 'block' ? 'none' : 'block';
}

function appendUserMessage(text) {
  var hist = document.getElementById('chat-history');
  var div = document.createElement('div');
  div.className = 'chat-msg user';
  div.dataset.raw = text;
  var bubble = document.createElement('div');
  bubble.className = 'bubble';
  var pre = document.createElement('pre');
  pre.textContent = text;
  bubble.appendChild(pre);
  div.appendChild(bubble);
  var actions = document.createElement('div');
  actions.className = 'msg-actions';
  actions.innerHTML = '<button type="button" class="act-edit" title="Edit message" onclick="editUserMessage(this)">Edit</button>';
  div.appendChild(actions);
  hist.appendChild(div);
  hist.scrollTop = hist.scrollHeight;
}

function editUserMessage(btn) {
  if (sending) return;
  var msgEl = btn.closest('.chat-msg.user');
  if (!msgEl) return;
  var original = msgEl.dataset.raw || '';
  var bubble = msgEl.querySelector('.bubble');
  var actions = msgEl.querySelector('.msg-actions');
  if (!bubble) return;
  bubble.style.display = 'none';
  if (actions) actions.style.display = 'none';
  var editor = document.createElement('div');
  editor.className = 'edit-area';
  editor.innerHTML =
    '<textarea></textarea>' +
    '<div class="buttons">' +
      '<button type="button" class="act-cancel">Cancel</button>' +
      '<button type="button" class="primary act-save">Send</button>' +
    '</div>';
  var ta = editor.querySelector('textarea');
  ta.value = original;
  editor.querySelector('.act-cancel').onclick = function() {
    editor.remove();
    bubble.style.display = '';
    if (actions) actions.style.display = '';
  };
  editor.querySelector('.act-save').onclick = function() {
    var next = ta.value.trim();
    if (!next) return;
    submitUserEdit(msgEl, next);
  };
  ta.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      editor.querySelector('.act-save').click();
    } else if (e.key === 'Escape') {
      editor.querySelector('.act-cancel').click();
    }
  });
  msgEl.appendChild(editor);
  ta.focus();
  ta.setSelectionRange(ta.value.length, ta.value.length);
}

// submitUserEdit rewrites history from a given user turn forward:
// drops the edited message and everything after it in both the DOM
// and chatHistory, then re-sends with the new text. The server-side
// session then gets truncated naturally on the next send because we
// POST the updated history.
function submitUserEdit(msgEl, newText) {
  var hist = document.getElementById('chat-history');
  // Count how many chat-msg elements precede msgEl; that's how many
  // history entries we should keep.
  var kept = 0;
  var all = hist.querySelectorAll('.chat-msg');
  for (var i = 0; i < all.length; i++) {
    if (all[i] === msgEl) break;
    if (all[i].classList.contains('user') || all[i].classList.contains('assistant')) kept++;
  }
  chatHistory = chatHistory.slice(0, kept);
  // Remove this message and every sibling after it.
  var node = msgEl;
  while (node) {
    var nxt = node.nextElementSibling;
    node.remove();
    node = nxt;
  }
  var input = document.getElementById('chat-input');
  input.value = newText;
  sendChat({replaceHistory: true});
}

// Create an empty assistant message placeholder that streams will fill in.
function createAssistantPlaceholder() {
  var hist = document.getElementById('chat-history');
  var div = document.createElement('div');
  div.className = 'chat-msg assistant';
  // Thinking dots appear while waiting for the first chunk or
  // tool_call event. removeThinkingDots() clears them on any
  // activity in the bubble.
  div.innerHTML = '<div class="thinking-dots"><span></span><span></span><span></span></div>'
    + '<pre class="content"></pre><div class="tools"></div>';
  hist.appendChild(div);
  hist.scrollTop = hist.scrollHeight;
  return div;
}

function removeThinkingDots(msgEl) {
  var dots = msgEl.querySelector('.thinking-dots');
  if (dots) dots.remove();
}

function appendChunk(msgEl, text) {
  removeThinkingDots(msgEl);
  var pre = msgEl.querySelector('.content');
  pre.textContent += text;
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendToolCall(msgEl, name, args) {
  removeThinkingDots(msgEl);
  var tools = msgEl.querySelector('.tools');
  var tc = document.createElement('details');
  tc.className = 'tool-call pending';
  tc.dataset.name = name;
  tc.innerHTML = '<summary><span class="name">' + escapeHtml(name) + '</span> <span class="tool-status">running…</span></summary>'
    + '<div class="tool-details">'
    + '<div class="args">args: ' + escapeHtml(args) + '</div>'
    + '<div class="result"></div>'
    + '</div>';
  tools.appendChild(tc);
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendToolResult(msgEl, name, result) {
  var tools = msgEl.querySelector('.tools');
  var pending = tools.querySelectorAll('.tool-call.pending');
  for (var i = pending.length - 1; i >= 0; i--) {
    if (pending[i].dataset.name === name) {
      pending[i].classList.remove('pending');
      pending[i].querySelector('.tool-status').textContent = '✓';
      pending[i].querySelector('.result').textContent = 'result: ' + result;
      var hist = document.getElementById('chat-history');
      hist.scrollTop = hist.scrollHeight;
      return;
    }
  }
}

function appendToolImage(msgEl, b64) {
  // Render a tool-produced image (screenshot, fetched image, generated
  // image) inline in the assistant's message. Click toggles full-size in
  // a new tab so cropped thumbnails stay readable.
  if (!msgEl || !b64) return;
  var imgs = msgEl.querySelector('.tool-images');
  if (!imgs) {
    imgs = document.createElement('div');
    imgs.className = 'tool-images';
    msgEl.appendChild(imgs);
  }
  var img = new Image();
  img.className = 'tool-image';
  img.src = 'data:image/png;base64,' + b64;
  img.title = 'click to open at full size';
  img.addEventListener('click', function() {
    var w = window.open();
    if (w) w.document.body.innerHTML = '<img src="' + img.src + '" style="max-width:100%">';
  });
  imgs.appendChild(img);
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendStatus(msgEl, text) {
  // Render a send_status progress note inline in the assistant bubble.
  // Visible while streaming so the user knows long-running work is in
  // flight; not persisted with the final transcript (the LLM's actual
  // reply is what gets saved).
  if (!msgEl || !text) return;
  removeThinkingDots(msgEl);
  var note = document.createElement('div');
  note.className = 'status-note';
  note.textContent = text;
  msgEl.appendChild(note);
  var hist = document.getElementById('chat-history');
  hist.scrollTop = hist.scrollHeight;
}

function appendError(msgEl, text) {
  if (msgEl) {
    removeThinkingDots(msgEl);
    msgEl.classList.add('error');
    var pre = msgEl.querySelector('.content');
    if (pre) pre.textContent += '\n[error] ' + text;
  } else {
    var hist = document.getElementById('chat-history');
    var div = document.createElement('div');
    div.className = 'chat-msg error';
    div.innerHTML = '<pre>[error] ' + escapeHtml(text) + '</pre>';
    hist.appendChild(div);
  }
}

function sendChat(opts) {
  if (sending) return;
  var input = document.getElementById('chat-input');
  var msg = input.value.trim();
  if (!msg) return;
  input.value = '';
  appendUserMessage(msg);
  sending = true;
  var btn = document.getElementById('chat-send');
  btn.disabled = true;
  btn.textContent = 'Thinking…';

  var assistantEl = createAssistantPlaceholder();
  var fullReply = '';
  var replaceHistory = !!(opts && opts.replaceHistory);

  fetch('api/send', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({session_id: currentSessionId, history: chatHistory, message: msg, replace_history: replaceHistory, private_mode: privateMode})
  }).then(function(response) {
    if (!response.ok) {
      return response.text().then(function(t) { throw new Error(t || 'HTTP ' + response.status); });
    }
    var reader = response.body.getReader();
    var decoder = new TextDecoder();
    var buffer = '';

    function handleEvent(eventType, data) {
      try { data = JSON.parse(data); } catch(e) { return; }
      if (eventType === 'session' && data.id) {
        // First event of every send — stash the server-assigned ID so
        // subsequent turns post into the same session, and refresh the
        // sidebar so the new row shows up right away.
        var wasNew = (currentSessionId !== data.id);
        currentSessionId = data.id;
        if (wasNew) loadSessions();
      } else if (eventType === 'chunk' && data.text) {
        fullReply += data.text;
        appendChunk(assistantEl, data.text);
      } else if (eventType === 'tool_call') {
        appendToolCall(assistantEl, data.name, data.args);
      } else if (eventType === 'tool_result') {
        appendToolResult(assistantEl, data.name, data.result);
      } else if (eventType === 'image' && data.data) {
        appendToolImage(assistantEl, data.data);
      } else if (eventType === 'status' && data.text) {
        appendStatus(assistantEl, data.text);
      } else if (eventType === 'done') {
        finishChat(true);
      } else if (eventType === 'error') {
        appendError(assistantEl, data.message || 'unknown error');
        finishChat(false);
      }
    }

    function finishChat(success) {
      sending = false;
      btn.disabled = false;
      btn.textContent = 'Send';
      // Replace the streaming <pre class="content"> with a rendered
      // markdown <div class="content md"> so ## headings, **bold**,
      // lists, and links look like formatted prose instead of raw
      // characters. Render only on completion because mid-stream
      // markdown is visually jumpy — half-open ** and ## produce
      // flicker until the closing token arrives. renderMarkdown is
      // the shared helper loaded from core/webui/static/base.js.
      if (assistantEl) {
        var pre = assistantEl.querySelector('.content');
        var finalText = '';
        if (pre) {
          finalText = pre.textContent.replace(/\s+$/, '');
          // Guard against binary / garbled LLM output — treat as empty.
          if (isGarbageText(finalText)) {
            finalText = '';
          }
          if (typeof renderMarkdown === 'function' && finalText) {
            var div = document.createElement('div');
            div.className = 'content md';
            div.innerHTML = renderMarkdown(finalText);
            pre.parentNode.replaceChild(div, pre);
          } else if (finalText) {
            pre.textContent = finalText;
          } else {
            pre.textContent = '(empty response)';
            pre.style.color = 'var(--text-mute)';
            pre.style.fontStyle = 'italic';
          }
        }
        if (success && finalText) addAssistantActions(assistantEl, finalText);
      }
      if (success && fullReply) {
        chatHistory.push({role: 'user', content: msg});
        chatHistory.push({role: 'assistant', content: fullReply.replace(/\s+$/, '')});
      }
      // Refresh the sidebar so a newly-saved session shows up and an
      // updated LastAt reorders existing rows. Schedule a second
      // refresh ~4s later to pick up the async-generated title for
      // brand-new sessions without polling forever.
      if (success) {
        loadSessions();
        if (sessionsRefreshTimer) clearTimeout(sessionsRefreshTimer);
        sessionsRefreshTimer = setTimeout(loadSessions, 4000);
      }
    }

    function pump() {
      return reader.read().then(function(result) {
        if (result.done) {
          // Stream ended without explicit done — treat as done if we got content,
          // otherwise finish as failure so the UI unsticks.
          if (sending) finishChat(fullReply.length > 0);
          return;
        }
        buffer += decoder.decode(result.value, {stream: true});
        // Parse SSE events: blocks separated by \n\n.
        var parts = buffer.split('\n\n');
        buffer = parts.pop(); // last part may be incomplete
        for (var i = 0; i < parts.length; i++) {
          var block = parts[i];
          var eventType = '';
          var dataLines = [];
          var lines = block.split('\n');
          for (var j = 0; j < lines.length; j++) {
            var line = lines[j];
            if (line.indexOf('event: ') === 0) {
              eventType = line.slice(7).trim();
            } else if (line.indexOf('data: ') === 0) {
              dataLines.push(line.slice(6));
            }
          }
          if (eventType && dataLines.length > 0) {
            handleEvent(eventType, dataLines.join('\n'));
          }
        }
        return pump();
      });
    }
    return pump();
  }).catch(function(err) {
    sending = false;
    btn.disabled = false;
    btn.textContent = 'Send';
    appendError(assistantEl, 'Request failed: ' + err.message);
  });
}

document.getElementById('chat-input').addEventListener('keydown', function(e){
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    sendChat();
  }
});

// handleAttachFile loads the selected file as text and inserts it into
// the chat input wrapped in a fenced block, with the filename called
// out so the LLM can refer to it and so it's clear what was pasted.
// Size cap: 256KB — browsers handle bigger but the chat request body
// gets unwieldy and the tool LLM has its own context limits. Only text
// files are accepted; binary content would just produce garbled prose.
function handleAttachFile(event) {
  var file = event.target.files && event.target.files[0];
  if (!file) return;
  var maxBytes = 256 * 1024;
  if (file.size > maxBytes) {
    alert('File too large (' + Math.round(file.size/1024) + 'KB). Limit is 256KB. Attach a smaller slice.');
    event.target.value = '';
    return;
  }
  var reader = new FileReader();
  reader.onload = function(e) {
    var text = e.target.result;
    if (typeof text !== 'string') { return; }
    var input = document.getElementById('chat-input');
    var prefix = input.value.trim() ? input.value.replace(/\s+$/, '') + '\n\n' : '';
    // Wrap in a fenced block with the filename as the header. \x60 is
    // the backtick literal — written escaped because this whole JS
    // blob lives inside a Go raw-string literal that uses backticks
    // as its delimiter, so a real backtick here would terminate the
    // Go string. The browser runs \x60 as U+0060 = backtick.
    var fence = '\x60\x60\x60';
    input.value = prefix + '--- ' + file.name + ' (' + file.size + ' bytes) ---\n' + fence + '\n' + text + '\n' + fence + '\n';
    input.focus();
    // Move cursor to end so the user can type their question right there.
    input.setSelectionRange(input.value.length, input.value.length);
  };
  reader.onerror = function() {
    alert('Failed to read file: ' + (reader.error && reader.error.message || 'unknown error'));
  };
  reader.readAsText(file);
  // Reset the file input so the same file can be re-attached later.
  event.target.value = '';
}

loadPrivateMode();
loadTools();
loadSessions();
// Default-collapse the sidebar on narrow viewports so the chat gets
// the full width on first load; desktop stays open.
if (window.matchMedia && window.matchMedia('(max-width: 600px)').matches) {
  toggleSidebar(false);
}
`
