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
	"net/http"
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

// turnDiag is appendSessionDiag bound to a chatTurn — the convenient form
// for guards firing inside a live turn. Nil-safe on every field.
func (t *chatTurn) turnDiag(kind, detail string) {
	if t == nil || t.session == nil {
		return
	}
	appendSessionDiag(t.udb, t.agent.ID, t.session.ID, kind, detail)
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
