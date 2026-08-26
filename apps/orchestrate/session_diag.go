// Per-session diagnostics trail — the framework's decisions made on the
// user's behalf inside one conversation: suppressed replies, discarded
// inputs, retries, reroutes. Guards that drop or rewrite content used to
// leave at best a server Debug line, which wiped exactly the evidence
// needed to diagnose "what went wrong" from the UI. Every guard now
// appends a bounded breadcrumb here; the chat panel's ⚠ affordance
// (ui.AgentLoopPanel.DiagnosticsURL) lists them per session. Cortex
// threads are sessions, so they get the same trail for free.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	sessionDiagTable = "session_diag"
	sessionDiagCap   = 50
)

// SessionDiag is one guard decision recorded against a session.
type SessionDiag struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// appendSessionDiag records one guard decision. Stored in its own table
// (NOT on the ChatSession record) deliberately: a mid-turn write onto the
// session struct would race the turn's own end-of-turn save and one side
// would clobber the other. Bounded ring (last sessionDiagCap entries);
// best-effort — a diagnostics write must never fail a real operation.
func appendSessionDiag(udb Database, agentID, sessionID, kind, detail string) {
	if udb == nil || strings.TrimSpace(agentID) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	key := agentID + ":" + sessionID
	var list []SessionDiag
	udb.Get(sessionDiagTable, key, &list)
	list = append(list, SessionDiag{At: time.Now(), Kind: kind, Detail: detail})
	if len(list) > sessionDiagCap {
		list = list[len(list)-sessionDiagCap:]
	}
	udb.Set(sessionDiagTable, key, list)
}

// beginDispatchDiag opens a DISPATCHED turn's diagnostics trail, and
// records the one thing a dispatch silently costs.
//
// The ids have to come from the caller: a dispatched turn has no
// *session of its own, and the record it should be filed against lives
// with whoever asked for the run. Every dispatch path wired these two
// fields by hand, which is also why this is the right place for the
// note below — a fourth path that wires diagnostics gets it for free,
// and one that does not was never going to record anything anyway.
//
// The note: a machine-driven agent dispatched (scheduled fire,
// delegation, phantom, sub-agent) runs WITHOUT its machine. That path
// assembles its own system prompt and there is no conversation to hold
// a position in, so there is no phase directive, no blackboard and no
// routing. It may be the right trade for a one-shot, but an agent whose
// whole procedure is its machine is not itself here, and a difference
// that large must not be silent.
func (t *chatTurn) beginDispatchDiag(agentID, sessionID string) {
	if t == nil {
		return
	}
	t.diagAgentID = agentID
	t.diagSessionID = sessionID
	m := strings.TrimSpace(t.agent.Machine)
	if m == "" {
		return
	}
	// The machine belongs to whoever authored the agent, not to the
	// runtime user a dispatch happens to run as (a phantom chat, a
	// scheduled job's account).
	db, owner := t.ownerDB, t.ownerUser
	if db == nil {
		db, owner = t.udb, t.user
	}
	name := m
	if def, ok := LoadMachineDef(db, owner, m); ok && strings.TrimSpace(def.Name) != "" {
		name = def.Name
	}
	t.turnDiag("machine_not_on_dispatch", "this agent runs the machine "+strconv.Quote(name)+
		", which needs a conversation to hold its position; a dispatched turn has none, so this one ran on the agent's persona alone")
}

// turnDiag is appendSessionDiag bound to a chatTurn — the convenient form
// for guards firing inside a live turn. Nil-safe on every field.
func (t *chatTurn) turnDiag(kind, detail string) {
	if t == nil {
		return
	}
	// A live turn writes to its own session. A BACKGROUND turn (scheduled fire,
	// monitor wake, dispatched sub-agent) has no *session at all — it was built
	// for the run, and the session record lives with the caller. Those turns run
	// the same guards, so requiring a *session silently discarded every
	// breadcrumb they left: the guardrail that stopped a 3am fire was in the
	// server log and nowhere a user would ever look.
	agentID, sessionID := t.agent.ID, ""
	if t.session != nil {
		sessionID = t.session.ID
	} else {
		if t.diagAgentID != "" {
			agentID = t.diagAgentID
		}
		sessionID = t.diagSessionID
	}
	if sessionID == "" {
		return // genuinely no trail to write to
	}
	// Write to the OWNER's store, not the runtime user's. A breadcrumb exists
	// for the person who configured the agent, and the trail is read back
	// through the requesting user's own store (handleSessionDiag) — so a turn
	// running as a synthetic per-chat identity that filed its diagnostics under
	// that identity filed them where nobody can ever look. Falls back to the
	// turn's own store for the ordinary case, where the two are the same.
	db := t.ownerDB
	if db == nil {
		db = t.udb
	}
	appendSessionDiag(db, agentID, sessionID, kind, detail)
}

// handleSessionDiag serves the trail: GET /api/session-diag?agent=&session=
// → [{at, kind, detail}], newest first. Scoped to the requesting user's own
// store, so one user can never read another's trail.
func (T *OrchestrateApp) handleSessionDiag(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	session := strings.TrimSpace(r.URL.Query().Get("session"))
	if agent == "" || session == "" {
		http.Error(w, "agent and session required", http.StatusBadRequest)
		return
	}
	var list []SessionDiag
	udb.Get(sessionDiagTable, agent+":"+session, &list)
	// Newest first for display.
	out := make([]SessionDiag, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	writeJSON(w, out)
}

// PublicHandleSessionDiag is the landing an app routes its AgentLoopPanel's
// DiagnosticsURL to. The agent id is the caller's, so an app cannot read
// another agent's trail by asking for it in the query string — which the
// admin-mounted variant can, because there the caller IS the operator.
//
// This is the answer to "why did that stop". The entries are the framework's
// decisions taken on the user's behalf inside one conversation — a denied
// tool, an approval that timed out, a guardrail that dropped a call — and
// without a surface they exist only in the server log, which is not where the
// person who asked the question is looking.
func (T *OrchestrateApp) PublicHandleSessionDiag(w http.ResponseWriter, r *http.Request, agentID, sessionID string) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if agentID == "" || sessionID == "" {
		http.Error(w, "agent and session required", http.StatusBadRequest)
		return
	}
	var list []SessionDiag
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	out := make([]SessionDiag, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// PublicHandleSessionRename renames one of the caller's sessions. Body is
// {id, name}, which is the shape core/ui's rename affordance POSTs — the id
// travels in the body rather than the path, so the URL needs no template.
//
// A coding thread's auto-title comes from its first message, which is
// routinely the least descriptive thing about it ("have a look at this").
func (T *OrchestrateApp) PublicHandleSessionRename(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || strings.TrimSpace(body.ID) == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	renameChatSession(udb, agentID, body.ID, body.Name)
	w.WriteHeader(http.StatusNoContent)
}
