package orchestrate

// The machine state drawer — what the phase pill opens.
//
// A session pinned to a machine carries eight pieces of state and the owner
// could see one: the phase name, as a pill. The blackboard (what earlier
// phases decided, and what every later step reads), the opening message, and
// the hop log with the reason for every transition were written each turn
// and rendered nowhere; the transitions surfaced only as prose in a 50-entry
// diagnostics ring shared with everything else. And there was no lever: no
// route could move a session to a phase, so a conversation parked in the
// wrong step was fixed by starting over.
//
// These four routes feed the panel's generic status click-through
// (AgentLoopPanel.StatusURL: detail_url + actions). The detail is the cursor
// laid out for a reader; the two actions move the cursor or clear the
// blackboard, each through the machine's own transition path and each
// leaving a breadcrumb in the session's diagnostics, so the walk's record
// and the owner's intervention are the same trail.

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// sessionStateQuery is what every route here takes.
func (T *OrchestrateApp) sessionStateQuery(w http.ResponseWriter, r *http.Request) (user string, udb Database, sess ChatSession, def MachineDef, ok bool) {
	user, udb, ok = RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if agentID == "" || sessionID == "" {
		http.Error(w, "agent and session are required", http.StatusBadRequest)
		return user, udb, sess, def, false
	}
	sess, found := loadChatSession(udb, agentID, sessionID)
	if !found || strings.TrimSpace(sess.MachineID) == "" {
		http.Error(w, "this conversation is not running a machine", http.StatusNotFound)
		return user, udb, sess, def, false
	}
	def, found = LoadMachineDef(udb, user, sess.MachineID)
	if !found {
		http.Error(w, "the machine this conversation was running has been deleted", http.StatusNotFound)
		return user, udb, sess, def, false
	}
	return user, udb, sess, def, true
}

// machineStatusActions is what the status pill carries beyond its label:
// the detail view and the two owner levers, with agent and session baked
// into each URL so the panel needs no substitution.
func machineStatusActions(agentID, sessionID string) (detail string, actions []map[string]any) {
	q := "?agent=" + url.QueryEscape(agentID) + "&session=" + url.QueryEscape(sessionID)
	detail = "api/session-state" + q
	actions = []map[string]any{
		{
			"label":       "Move to phase",
			"url":         "api/session-phase" + q,
			"method":      "POST",
			"options_url": "api/session-phases" + q,
			"confirm":     "Move this conversation to the chosen phase? Earlier results stay on the blackboard; the phase's own Keep list applies as on any re-entry, and the move is recorded in the transition log.",
		},
		{
			"label":   "Clear blackboard",
			"url":     "api/session-state-clear" + q,
			"method":  "POST",
			"variant": "danger",
			"confirm": "Forget every earlier phase result pinned to this conversation? The phase stays where it is; later steps will no longer see what earlier ones decided.",
		},
	}
	return detail, actions
}

// sessionStateView is the drawer's content, in the order a reader wants it:
// where the walk is, what it started from, what it has decided, how it got
// here.
type sessionStateView struct {
	Machine     string             `json:"Machine"`
	Phase       string             `json:"Phase"`
	Path        string             `json:"Path"`
	Opening     string             `json:"Opening message,omitempty"`
	Blackboard  []sessionPhaseView `json:"Blackboard,omitempty"`
	Transitions []sessionHopView   `json:"Transitions,omitempty"`
	Note        string             `json:"Note,omitempty"`
	// Context is what the thread actually carries into a turn (session_context.go),
	// shown under the machine's state so one drawer answers both "where is the
	// walk" and "what does it still remember".
	Context *sessionContextView `json:"Context,omitempty"`
}

type sessionPhaseView struct {
	Phase  string         `json:"phase"`
	Text   string         `json:"text,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

type sessionHopView struct {
	When string `json:"when"`
	Move string `json:"move"`
	Why  string `json:"why,omitempty"`
}

// handleSessionState serves the drawer.
//
//	GET /api/session-state?agent=<id>&session=<id>
func (T *OrchestrateApp) handleSessionState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, udb, sess, def, ok := T.sessionStateQuery(w, r)
	if !ok {
		return
	}
	loc := UserLocation(user)
	view := sessionStateView{
		Machine: def.Name,
		Phase:   sess.Phase,
		Path:    machinePathLine(def, sess.Phase),
		Opening: sess.MachineOpening,
	}
	// Blackboard in the machine's own phase order, then anything the def no
	// longer names (an accumulator, a renamed phase) after it.
	order := map[string]int{}
	for i, ph := range def.Phases {
		order[ph.Name] = i
	}
	names := make([]string, 0, len(sess.MachineState))
	for name := range sess.MachineState {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		oi, iok := order[names[i]]
		oj, jok := order[names[j]]
		if iok != jok {
			return iok
		}
		if iok && oi != oj {
			return oi < oj
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		res := sess.MachineState[name]
		view.Blackboard = append(view.Blackboard, sessionPhaseView{Phase: name, Text: res.Text, Fields: res.Fields})
	}
	// Newest transition first; the one that put the walk where it is reads
	// before the ones that led up to it.
	for i := len(sess.MachineLog) - 1; i >= 0; i-- {
		h := sess.MachineLog[i]
		when := ""
		if !h.At.IsZero() {
			when = h.At.In(loc).Format("Jan 2 15:04:05")
		}
		view.Transitions = append(view.Transitions, sessionHopView{When: when, Move: h.From + " → " + h.To, Why: h.Why})
	}
	if len(sess.MachineState) == 0 && len(sess.MachineLog) == 0 {
		view.Note = "No phase has completed yet; the walk is at its first step."
	}
	if cv := sessionContextOf(udb, sess); cv != nil {
		view.Context = cv
	}
	writeJSON(w, view)
}

// machinePathLine renders the machine's phases in order with the current
// one marked, so the drawer says where the walk is in one line.
func machinePathLine(def MachineDef, current string) string {
	parts := make([]string, 0, len(def.Phases))
	for _, ph := range def.Phases {
		if ph.Name == current {
			parts = append(parts, "["+ph.Name+"]")
		} else {
			parts = append(parts, ph.Name)
		}
	}
	return strings.Join(parts, " → ")
}

// handleSessionPhases lists the phases the owner may move to.
//
//	GET /api/session-phases?agent=<id>&session=<id> → [{value,label}]
func (T *OrchestrateApp) handleSessionPhases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _, sess, def, ok := T.sessionStateQuery(w, r)
	if !ok {
		return
	}
	out := make([]map[string]any, 0, len(def.Phases))
	for _, ph := range def.Phases {
		label := ph.Name
		if d := strings.TrimSpace(ph.Desc); d != "" {
			label += " — " + truncateObs(d, 60)
		}
		if ph.Name == sess.Phase {
			label += " (current)"
		}
		out = append(out, map[string]any{"value": ph.Name, "label": label})
	}
	writeJSON(w, out)
}

// handleSessionPhase moves the cursor.
//
//	POST /api/session-phase?agent=<id>&session=<id>&value=<phase>
func (T *OrchestrateApp) handleSessionPhase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, sess, def, ok := T.sessionStateQuery(w, r)
	if !ok {
		return
	}
	to := strings.TrimSpace(r.URL.Query().Get("value"))
	if to == "" {
		http.Error(w, "value (the phase to move to) is required", http.StatusBadRequest)
		return
	}
	if to == sess.Phase {
		writeJSON(w, map[string]any{"ok": true, "phase": sess.Phase, "note": "already there"})
		return
	}
	cur := &MachineCursor{Phase: sess.Phase, State: sess.MachineState, Log: sess.MachineLog, Opening: sess.MachineOpening}
	note := func(kind, detail string) { appendSessionDiag(udb, sess.AgentID, sess.ID, kind, detail) }
	from := sess.Phase
	ph, err := def.MoveCursor(cur, to, "moved by the owner from the console", note)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess.Phase = cur.Phase
	sess.MachineState = cur.State
	sess.MachineLog = cur.Log
	if _, err := saveChatSession(udb, sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	appendSessionDiag(udb, sess.AgentID, sess.ID, "machine_phase_moved_by_owner",
		"the owner moved this conversation from step "+from+" to "+ph.Name+" from the console; the next turn runs there")
	writeJSON(w, map[string]any{"ok": true, "phase": ph.Name})
}

// handleSessionStateClear forgets the blackboard.
//
//	POST /api/session-state-clear?agent=<id>&session=<id>
func (T *OrchestrateApp) handleSessionStateClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, sess, _, ok := T.sessionStateQuery(w, r)
	if !ok {
		return
	}
	n := len(sess.MachineState)
	sess.MachineState = nil
	if _, err := saveChatSession(udb, sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	appendSessionDiag(udb, sess.AgentID, sess.ID, "machine_state_cleared_by_owner",
		"the owner cleared the blackboard from the console ("+strconv.Itoa(n)+" phase result(s) forgotten); the phase is unchanged at "+sess.Phase)
	writeJSON(w, map[string]any{"ok": true, "cleared": n, "at": time.Now().UTC().Format(time.RFC3339)})
}
