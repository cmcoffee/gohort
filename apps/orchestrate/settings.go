// Per-user (and per-(user, agent)) Private + Memory toggle preferences
// for the orchestrate chat surface. The toggles are scoped per agent
// when ?agent_id=<id> is provided; without that param they read/write
// the global per-user fallback.
//
// Endpoint shape:
//
//	GET  /api/settings/private[?agent_id=X]       → {private_mode: bool}
//	POST /api/settings/private/set[?agent_id=X]   {private_mode: bool} → echo
//	GET  /api/settings/memory[?agent_id=X]        → {inferred_disabled: bool}
//	POST /api/settings/memory/set[?agent_id=X]    {inferred_disabled: bool} → echo
//
// The body may also carry agent_id (matches Chat's send-body convention)
// — when present, it takes precedence over the query param.
//
// The "memory" toggle controls the Reference Memory layer (LLM-derived
// vector chunks via memory_save + synthesis auto-ingest). Knowledge
// (uploaded files) and Explicit Memory (always-in-prompt facts) are
// agent-config concerns, not per-turn toggles.
//
// AuthDB() — not the per-app bucket — owns the user records; the
// per-app DB would always miss the lookup.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// resolveAgentID picks an agent id from the query string or request
// body, whichever is non-empty. Body wins when both are set so a
// JS caller can override the URL param without re-encoding. Returns
// "" when neither is provided — the call falls back to global toggle.
func resolveAgentID(r *http.Request, bodyAgentID string) string {
	id := strings.TrimSpace(bodyAgentID)
	if id != "" {
		return id
	}
	return strings.TrimSpace(r.URL.Query().Get("agent_id"))
}

func (T *OrchestrateApp) handlePrivateModeGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := resolveAgentID(r, "")
	mode := AuthGetPrivateModeForAgent(AuthDB(), user, agentID)
	// When the agent has ForcePrivate=true, the toggle is locked ON
	// regardless of per-agent override or global value. Surface this
	// to the JS so the UI can hide the toggle (or render it visibly
	// locked) — a togglable button that ignores clicks is worse than
	// no button at all.
	locked := false
	if agentID != "" {
		if a, ok := loadAgent(udb, agentID); ok && a.ForcePrivate {
			mode = true
			locked = true
		}
	}
	Log("[orchestrate/settings] GET private user=%q agent_id=%q → %v (locked=%v)", user, agentID, mode, locked)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"private_mode": mode,
		"locked":       locked,
	})
}

func (T *OrchestrateApp) handlePrivateModeSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		PrivateMode bool   `json:"private_mode"`
		AgentID     string `json:"agent_id"`
		// SessionID (optional) — when provided AND a turn is
		// in-flight for that session, flip the active connector
		// live. Without this, the toggle only affects future turns.
		// The chat surface passes the active session id when the
		// user flips Private mid-turn for emergency cutoff.
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	agentID := resolveAgentID(r, req.AgentID)
	Log("[orchestrate/settings] SET private user=%q agent_id=%q (body=%q query=%q) value=%v sid=%q",
		user, agentID, req.AgentID, r.URL.Query().Get("agent_id"), req.PrivateMode, req.SessionID)
	AuthSetPrivateModeForAgent(AuthDB(), user, agentID, req.PrivateMode)
	// Live cutoff: if a turn is in-flight for the named session,
	// flip its connector now. Allowed = !privateMode (private ON
	// → connector blocks). Logs whether the flip actually landed
	// so the client can confirm the cutoff applied.
	liveFlipped := false
	if req.SessionID != "" {
		if v, ok := inflightConnectors.Load(req.SessionID); ok {
			if conn, ok := v.(*NetworkConnector); ok && conn != nil {
				conn.SetAllowed(!req.PrivateMode)
				liveFlipped = true
				Log("[orchestrate/settings] live cutoff applied to inflight session %s — connector allowed=%v", req.SessionID, !req.PrivateMode)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"private_mode": req.PrivateMode,
		"live_flipped": liveFlipped,
	})
}

// handleMemoryModeGet / Set expose the (per-(user, agent)) Inferred
// Memory suppression preference. When the user toggles this on for a
// specific agent, that agent's runs drop memory_save / memory_search /
// memory_forget tools AND skip synthesis auto-ingest AND exclude
// derived chunks from auto-injection. Other agents the same user owns
// keep their own (different) settings.
//
// Locked when agent.DisableInferred is set — the agent is configured
// to never use the Reference Memory layer, so the per-turn toggle is
// moot. Composes with the runtime gate: t.inferredOff() returns true
// if EITHER the per-turn toggle OR DisableInferred is set.
func (T *OrchestrateApp) handleMemoryModeGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := resolveAgentID(r, "")
	disabled := AuthGetInferredDisabledForAgent(AuthDB(), user, agentID)
	locked := false
	if agentID != "" {
		if a, ok := loadAgent(udb, agentID); ok && a.DisableInferred {
			disabled = true
			locked = true
		}
	}
	Log("[orchestrate/settings] GET memory user=%q agent_id=%q → %v (locked=%v)", user, agentID, disabled, locked)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"inferred_disabled": disabled,
		"locked":            locked,
	})
}

func (T *OrchestrateApp) handleMemoryModeSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		InferredDisabled bool   `json:"inferred_disabled"`
		AgentID          string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	agentID := resolveAgentID(r, req.AgentID)
	Log("[orchestrate/settings] SET memory user=%q agent_id=%q (body=%q query=%q) value=%v", user, agentID, req.AgentID, r.URL.Query().Get("agent_id"), req.InferredDisabled)
	AuthSetInferredDisabledForAgent(AuthDB(), user, agentID, req.InferredDisabled)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"inferred_disabled": req.InferredDisabled})
}
