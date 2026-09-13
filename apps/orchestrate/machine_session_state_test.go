package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The drawer shows the whole cursor — blackboard in phase order, transitions
// newest first with their reasons — and the owner's move goes through the
// machine's own transition path, leaving a hop and a breadcrumb.
func TestMachineStateDrawerAndOwnerMove(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Triage", Start: "gather",
		Phases: []MachinePhase{{Name: "gather", Desc: "Collect the facts"}, {Name: "decide"}, {Name: "report", Resident: true}}})
	sess, err := saveChatSession(udb, ChatSession{AgentID: "agent-1", Title: "t", MachineID: def.ID, Phase: "decide",
		MachineOpening: "what broke overnight?",
		MachineState:   MachineState{"gather": {Text: "three alerts"}, "decide": {Fields: map[string]any{"severity": "high"}}},
		MachineLog:     []PhaseHop{{From: "gather", To: "decide", Why: "exit condition met"}}})
	if err != nil {
		t.Fatal(err)
	}
	q := "?agent=agent-1&session=" + sess.ID
	call := func(method, path string, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := asUser(httptest.NewRequest(method, path, nil), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	// The pill now carries the door and the levers.
	w := call(http.MethodGet, "/api/session-status"+q, T.handleSessionStatus)
	var pill map[string]any
	json.Unmarshal(w.Body.Bytes(), &pill)
	if pill["label"] != "decide" || !strings.HasPrefix(pill["detail_url"].(string), "api/session-state?") {
		t.Fatalf("pill = %v", pill)
	}
	if acts, _ := pill["actions"].([]any); len(acts) != 2 {
		t.Fatalf("actions = %v", pill["actions"])
	}

	w = call(http.MethodGet, "/api/session-state"+q, T.handleSessionState)
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	var view sessionStateView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Phase != "decide" || view.Path != "gather → [decide] → report" || view.Opening != "what broke overnight?" {
		t.Fatalf("view = %+v", view)
	}
	if len(view.Blackboard) != 2 || view.Blackboard[0].Phase != "gather" || view.Blackboard[1].Fields["severity"] != "high" {
		t.Fatalf("blackboard = %+v", view.Blackboard)
	}
	if len(view.Transitions) != 1 || view.Transitions[0].Move != "gather → decide" || view.Transitions[0].Why != "exit condition met" {
		t.Fatalf("transitions = %+v", view.Transitions)
	}

	w = call(http.MethodGet, "/api/session-phases"+q, T.handleSessionPhases)
	var opts []map[string]any
	json.Unmarshal(w.Body.Bytes(), &opts)
	if len(opts) != 3 || opts[0]["label"] != "gather — Collect the facts" || !strings.HasSuffix(opts[1]["label"].(string), "(current)") {
		t.Fatalf("phases = %v", opts)
	}

	// Move back to gather: a hop with the owner's reason, the breadcrumb, and
	// the pill following.
	w = call(http.MethodPost, "/api/session-phase"+q+"&value=gather", T.handleSessionPhase)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	moved, _ := loadChatSession(udb, "agent-1", sess.ID)
	if moved.Phase != "gather" || len(moved.MachineLog) != 2 || moved.MachineLog[1].Why != "moved by the owner from the console" {
		t.Fatalf("session after move = phase %q log %+v", moved.Phase, moved.MachineLog)
	}
	var diags []SessionDiag
	udb.Get(sessionDiagTable, "agent-1:"+sess.ID, &diags)
	found := false
	for _, d := range diags {
		if d.Kind == "machine_phase_moved_by_owner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no breadcrumb for the owner's move: %+v", diags)
	}
	// An unknown phase is refused and nothing moves.
	if w = call(http.MethodPost, "/api/session-phase"+q+"&value=nope", T.handleSessionPhase); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown phase: %d", w.Code)
	}

	// Clearing the blackboard keeps the phase and leaves its own breadcrumb.
	w = call(http.MethodPost, "/api/session-state-clear"+q, T.handleSessionStateClear)
	if w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	cleared, _ := loadChatSession(udb, "agent-1", sess.ID)
	if len(cleared.MachineState) != 0 || cleared.Phase != "gather" {
		t.Fatalf("after clear = %+v", cleared)
	}
}

// A session with no machine has no drawer.
func TestMachineStateRoutesRefuseAnOrdinarySession(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	sess, _ := saveChatSession(udb, ChatSession{AgentID: "agent-1", Title: "plain"})
	r := asUser(httptest.NewRequest(http.MethodGet, "/api/session-state?agent=agent-1&session="+sess.ID, nil), user)
	w := httptest.NewRecorder()
	T.handleSessionState(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("plain session: %d", w.Code)
	}
}
