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

// sessionListItem is the wire shape returned by handleSessionList.
// Embeds ChatSession for the native rows; the optional Source +
// ChatID fields carry tagging contributed by external sources (see
// core/session_sources.go — phantom uses these so its dispatched
// sessions surface in Agency's rail with a per-chat badge).
//
// Running marks a session as currently mid-turn — i.e. the runs
// registry has an in-flight Run for this session ID. The rail uses
// this to render an "active" indicator so the user can see, at a
// glance, which sessions are still working after a disconnect /
// reconnect. Computed at request time from the run registry, not
// persisted on the session record.
type sessionListItem struct {
	ChatSession
	Source  string `json:"source,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	Running bool   `json:"running,omitempty"`
}

// handleSessionList returns the chat sessions for the active agent.
// Returns an empty list (not 400) when agent_id is blank so the
// AgentLoopPanel's first fetch — before the user picks an agent —
// renders cleanly instead of as an error toast.
//
// The response merges native sessions (this user's DB) with rows
// contributed by any registered ExtraSessionsSource (phantom today).
// External rows carry source + chat_id tags so the rail can render
// per-source visual markers.
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
		_ = json.NewEncoder(w).Encode([]sessionListItem{})
		return
	}
	agent, ok := loadAgent(udb, id)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		_ = json.NewEncoder(w).Encode([]sessionListItem{})
		return
	}
	native := listChatSessions(udb, agent.ID)
	out := make([]sessionListItem, 0, len(native))
	runs := T.runsRegistry()
	for _, s := range native {
		item := sessionListItem{ChatSession: s}
		// Running flag: in-flight Run keyed by session ID. BySession
		// returns nil for unknown / no-run-active. Cheap map lookup
		// per session — fine for typical list sizes.
		if r := runs.BySession(s.ID); r != nil && r.Status() == RunStatusRunning {
			item.Running = true
		}
		out = append(out, item)
	}
	for _, ext := range CollectExtraSessions(T.DB, agent.ID, user) {
		item := sessionListItem{
			ChatSession: ChatSession{
				ID:      ext.ID,
				AgentID: ext.AgentID,
				Title:   ext.Title,
				LastAt:  ext.LastAt,
			},
			Source: ext.Source,
			ChatID: ext.ChatID,
		}
		if r := runs.BySession(ext.ID); r != nil && r.Status() == RunStatusRunning {
			item.Running = true
		}
		out = append(out, item)
	}
	_ = json.NewEncoder(w).Encode(out)
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
	// Detect /api/sessions/{sid}/send-to-builder — stage an improvement
	// brief (framing + full transcript) and hand it to the Builder
	// agent so it can diagnose where this agent fell short and fix it.
	if strings.HasSuffix(sid, "/send-to-builder") {
		sid = strings.TrimSuffix(sid, "/send-to-builder")
		if sid == "" || strings.Contains(sid, "/") {
			http.NotFound(w, r)
			return
		}
		agent, ok := T.resolveAgent(w, r, udb, user)
		if !ok {
			return
		}
		T.handleSendToBuilder(w, r, agent, sid)
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
	// Channel agents no longer force every visit onto one thread — they have
	// a channel thread AND normal sessions. The channel thread is just a
	// specific session id (channelSessionID) the client opens via the
	// Channel row; ad-hoc sessions pass through with their own ids. So we
	// don't rewrite sid here anymore.
	switch r.Method {
	case http.MethodGet:
		// External-source row (e.g. ?source=phantom&chat_id=…)?
		// Delegate to the registered source so it can resolve to
		// the right per-source storage scope. Sources gate user
		// permission internally; on a false result we serve the
		// same 404 native misses get, so a leaked source name
		// can't enumerate other users' rows.
		if src := strings.TrimSpace(r.URL.Query().Get("source")); src != "" {
			if source, found := LookupExtraSessionsSource(src); found {
				if payload, ok := source.LoadSession(T.DB, agent.ID, user, sid, r.URL.Query().Get("chat_id")); ok {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(payload)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		s, ok := loadChatSession(udb, agent.ID, sid)
		if !ok {
			// The channel thread doesn't exist until its first turn; when the
			// Channel row opens it, return an empty session instead of 404 so
			// the client opens a fresh thread rather than erroring. Ad-hoc
			// sessions still 404 on a miss.
			if agent.Channel && sid == channelSessionID(agent.ID) {
				s = ChatSession{ID: sid}
			} else {
				http.NotFound(w, r)
				return
			}
		}
		// Opening a session clears its unread state (a background wake that
		// landed here is now seen). Writes LastSeen only — not activity.
		markSessionSeen(udb, agent.ID, sid)
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
		// Truncating the whole thread away (At==0) must also drop the rolling
		// summary — otherwise the orchestrator's compaction digest survives the
		// clear and re-primes every turn (the "stuck personality" trap). No-op
		// for agents with no compact-state row.
		if body.At == 0 {
			deleteCompactState(udb, agent.ID, sid)
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
