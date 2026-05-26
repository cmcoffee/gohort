// Flat API surface for the agent-dropdown-driven chat page. The active
// agent rides on every request via the `agent_id` query string (GET /
// DELETE) or JSON body field (POST). Authorization always checks
// owner — users can't reach into another user's agent buckets by
// guessing IDs, and the seed pseudo-owner ("system") is read-allowed
// for everybody.

package orchestrate

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/tools/temptool"

	. "github.com/cmcoffee/gohort/core"
)

// resolveAgent loads the agent named by `agent_id` (query first, then
// JSON body for POSTs) and authorizes it against the user. Writes the
// appropriate HTTP error and returns ok=false on any failure so
// callers can early-return.
func (T *OrchestrateApp) resolveAgent(w http.ResponseWriter, r *http.Request, udb Database, user string) (AgentRecord, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	// POST bodies — we need to peek without consuming the stream the
	// real handler will read. Cap the peek so a giant body can't OOM
	// us. The handlers that read the body re-decode from r.Body, so
	// we restore it via a NopCloser after the peek.
	if id == "" && r.Method == http.MethodPost {
		// Try the body. Reset it after.
		var head struct {
			AgentID string `json:"agent_id"`
		}
		raw, err := readAndRestoreBody(r)
		if err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &head)
			id = strings.TrimSpace(head.AgentID)
		}
	}
	if id == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return AgentRecord{}, false
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return AgentRecord{}, false
	}
	return agent, true
}

// handleSessionList returns the chat sessions for the active agent.
// Returns an empty list (not 400) when agent_id is blank so the
// AgentLoopPanel's first fetch — before the user picks an agent —
// renders cleanly instead of as an error toast.
func (T *OrchestrateApp) handleSessionList(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	w.Header().Set("Content-Type", "application/json")
	if id == "" {
		_ = json.NewEncoder(w).Encode([]ChatSession{})
		return
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		_ = json.NewEncoder(w).Encode([]ChatSession{})
		return
	}
	_ = json.NewEncoder(w).Encode(listChatSessions(udb, agent.ID))
}

// handleSessionOne loads or deletes a session in the active agent's
// bucket. agent_id is required on both methods.
func (T *OrchestrateApp) handleSessionOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	// Detect /api/sessions/{sid}/export — a sub-action that dumps
	// the full session as JSON or markdown for sharing / debugging.
	if strings.HasSuffix(sid, "/export") {
		sid = strings.TrimSuffix(sid, "/export")
		if sid == "" || strings.Contains(sid, "/") {
			http.NotFound(w, r)
			return
		}
		agent, ok := T.resolveAgent(w, r, udb, user)
		if !ok {
			return
		}
		T.handleSessionExport(w, r, agent.ID, sid)
		return
	}
	// Detect /api/sessions/{sid}/tools[/{name}] — visibility +
	// admin-driven promotion of session-scoped temp tools out of the
	// per-session draft pool into either the agent's bundled tools
	// (global=false) or the user-wide persistent pool (global=true).
	if idx := strings.Index(sid, "/tools"); idx >= 0 {
		actualSid := sid[:idx]
		rest := sid[idx+len("/tools"):]
		if actualSid == "" || strings.Contains(actualSid, "/") {
			http.NotFound(w, r)
			return
		}
		agent, ok := T.resolveAgent(w, r, udb, user)
		if !ok {
			return
		}
		if rest == "" {
			T.handleSessionToolsList(w, r, udb, user, agent.ID, actualSid)
			return
		}
		toolName := strings.TrimPrefix(rest, "/")
		if toolName == "" || strings.Contains(toolName, "/") {
			http.NotFound(w, r)
			return
		}
		T.handleSessionToolAction(w, r, udb, user, agent.ID, actualSid, toolName)
		return
	}
	if sid == "" || strings.Contains(sid, "/") {
		http.NotFound(w, r)
		return
	}
	agent, ok := T.resolveAgent(w, r, udb, user)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s, ok := loadChatSession(udb, agent.ID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	case http.MethodDelete:
		// Tear down any persistent shells the session opened
		// (psql/redis-cli/ssh/etc.) so the bwrap processes don't
		// leak. Scope is sess.ChatSessionID — matches what the
		// persistent_shell substrate uses as its registry key prefix.
		temptool.TerminateSessionShellsByScope(sid)
		deleteChatSession(udb, agent.ID, sid)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		// PATCH = truncate at a message index. Body: {at: N} drops
		// messages from index N onward. Used by per-message Edit /
		// Delete affordances in the chat surface: client drops the
		// DOM rows, server drops the persisted rows, and a follow-up
		// send (on Edit) appends a fresh user message + assistant
		// response below the truncation point.
		var body struct {
			At int `json:"at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s, ok := loadChatSession(udb, agent.ID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body.At < 0 {
			body.At = 0
		}
		if body.At > len(s.Messages) {
			body.At = len(s.Messages)
		}
		s.Messages = s.Messages[:body.At]
		if _, err := saveChatSession(udb, s); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"at": body.At, "messages_remaining": len(s.Messages)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleSendRouter is the flat-route wrapper around the runner. Reads
// agent_id from the body, loads + authorizes the agent, then hands
// off to the (unchanged) per-agent runner.
func (T *OrchestrateApp) handleSendRouter(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := T.resolveAgent(w, r, udb, user)
	if !ok {
		return
	}
	T.handleSend(w, r, udb, user, agent)
}

// handleCancelRouter wraps the runner's cancel. The cancel handler only
// needs the session id (which keys the in-flight cancel map); agent_id
// isn't strictly required, but we still authorize it when supplied so
// users can't cancel another owner's session by guessing its id.
func (T *OrchestrateApp) handleCancelRouter(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := T.resolveAgent(w, r, udb, user)
	if !ok {
		return
	}
	T.handleCancel(w, r, agent)
}

// handleConfirmRouter is the Phase 1 no-op confirm endpoint. Plans
// auto-confirm so there's nothing to wait on; the route still exists
// so AgentLoopPanel's runtime can POST without 404ing.
func (T *OrchestrateApp) handleConfirmRouter(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// readAndRestoreBody slurps r.Body into memory and replaces r.Body
// with a fresh reader over the same bytes so the downstream handler
// can read it normally. Used by resolveAgent to peek the JSON body
// for agent_id without consuming the stream.
//
// Cap at 1MB — orchestrate POST bodies are small (a user message
// plus a few flags); anything larger is suspect and shouldn't be
// silently buffered.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	const max = 1 << 20
	buf, err := io.ReadAll(io.LimitReader(r.Body, max))
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}
