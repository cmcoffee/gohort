package orchestrate

// The Broken tools pane.
//
// The outcome ledger (tool_outcome_ledger.go) has known for a while which
// actions fail every time they are called; the only thing it did with the
// knowledge was drop one breadcrumb on the fifth failure, into whichever
// thread happened to trip it, and hand the model a hint on every failure
// after. brokenToolActions — "for a surface that wants to show a user what
// needs fixing" — had no caller. This is the surface: every action in the
// owner's tally that has failed repeatedly and never once succeeded, worst
// first, with the last error in view, and a Forget for the one they have
// just fixed.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// consoleBrokenToolRow is one card. Field order is display order: the action
// is the title, Status the pill, the rest muted detail; _id is Forget's key.
type consoleBrokenToolRow struct {
	Action   string `json:"action"`
	Status   string `json:"Status"`
	Failures string `json:"failures"`
	Since    string `json:"since"`
	Error    string `json:"last_error,omitempty"`
	ID       string `json:"_id"`
}

// toolOutcomeStore is where the ledger lives: the auth store, keyed by
// username — the store every ToolSession is built with, so a tally written
// during a run is the one the console reads.
func toolOutcomeStore() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// handleConsoleBrokenTools lists the owner's broken actions.
func (T *OrchestrateApp) handleConsoleBrokenTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	loc := UserLocation(user)
	rows := []consoleBrokenToolRow{}
	for _, rec := range brokenToolActions(toolOutcomeStore(), user) {
		rows = append(rows, consoleBrokenToolRow{
			Action:   rec.Tool + " · " + rec.Action,
			Status:   "broken",
			Failures: fmt.Sprintf("%d failure(s), never succeeded", rec.Fail),
			Since:    brokenToolSince(rec, loc),
			Error:    rec.LastError,
			ID:       rec.key(),
		})
	}
	writeJSON(w, rows)
}

// brokenToolSince renders the window the failures span, in the owner's zone.
func brokenToolSince(rec toolOutcomeRecord, loc *time.Location) string {
	if rec.FirstAt.IsZero() {
		return ""
	}
	first := rec.FirstAt.In(loc).Format("Jan 2 15:04")
	if rec.LastAt.IsZero() || rec.LastAt.Sub(rec.FirstAt) < time.Minute {
		return "at " + first
	}
	return "from " + first + " to " + rec.LastAt.In(loc).Format("Jan 2 15:04")
}

// handleConsoleBrokenToolForget clears one action's tally.
//
//	POST /api/console/broken-tools/forget?id=<tool.action>
func (T *OrchestrateApp) handleConsoleBrokenToolForget(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("id"))
	if key == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if !forgetToolOutcome(toolOutcomeStore(), user, key) {
		http.Error(w, "no tally for that action (it may already have cleared itself with a success)", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "forgot": key})
}
