package orchestrate

// One page for everything that runs on a clock.
//
// Scheduled agents, recurring tasks and event monitors were three nav entries
// in each of two menus — six ways in to what a person thinks of as one
// question, "what is this agent going to do on its own". They are not the same
// RECORD (a standing agent has a mission, a recurring task has a fire count, a
// monitor has a condition) and this does not pretend otherwise: each keeps its
// own fields and its own actions. What it stops doing is making the reader
// discover that by opening three lists.
//
// Deliberately NOT a fourth row builder. It calls the same three the single
// views call and tags what comes back, because a merged view with its own copy
// of the logic is how a page comes to disagree with the page beside it about
// what is scheduled.
//
// Everything about the presentation is generic and already existed: the cards
// layout draws a "_section" heading each time the value changes, and reads each
// row's OWN visible keys, so three shapes render in one list without core/ui
// learning what any of them is.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Section headings, in the order the page draws them. Clock-driven work first,
// because that is what most people come looking for; the condition-driven
// monitors read as the different kind of thing they are at the bottom.
const (
	schedSectionStanding  = "Scheduled agents"
	schedSectionRecurring = "Recurring tasks"
	schedSectionMonitors  = "Event monitors"
)

// handleConsoleScheduler serves the merged view: GET /api/console/scheduler
// (+ ?agent= to scope it, which the Manage menu does and the Fleet menu
// deliberately does not).
func (T *OrchestrateApp) handleConsoleScheduler(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	out := []map[string]any{}
	for _, row := range consoleAgentRows(user, udb, agentID) {
		out = appendSchedulerRow(out, row, schedSectionStanding, schedKindStanding)
	}
	for _, row := range consoleRecurringRows(user, agentID) {
		out = appendSchedulerRow(out, row, schedSectionRecurring, schedKindRecurring)
	}
	for _, row := range consoleMonitorRows(user, agentID) {
		out = appendSchedulerRow(out, row, schedSectionMonitors, schedKindMonitor)
	}
	writeJSON(w, out)
}

// The three kinds a row can be. Used only to pick which action flags to
// compose; the SECTION is what the reader sees.
const (
	schedKindStanding  = "standing"
	schedKindRecurring = "recurring"
	schedKindMonitor   = "monitor"
)

// appendSchedulerRow converts one typed row to the merged shape and appends it.
// A row that will not marshal is dropped rather than half-rendered — the list
// is a status view, and a card missing the fields that say what it is would
// read as a broken schedule rather than a broken conversion.
func appendSchedulerRow(out []map[string]any, row any, section, kind string) []map[string]any {
	m := schedulerRow(row, section, kind)
	if m == nil {
		return out
	}
	return append(out, m)
}

// schedulerRow re-shapes a typed console row into the merged one: its own
// fields, its section, and the per-action flags.
//
// Through JSON rather than a hand-written field copy, so the merged view
// renders EXACTLY what the single view renders — including the omitempty rules
// — and a field added to one of the three row types appears here without
// anybody remembering to add it.
func schedulerRow(row any, section, kind string) map[string]any {
	b, err := json.Marshal(row)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	m["_section"] = section
	addSchedulerActionFlags(m, kind)
	return m
}

// addSchedulerActionFlags composes one flag per row action.
//
// A row action gates on ONE field (only_if / hide_if), and the merged list
// needs conditions that are two facts joined — Resume belongs on a PAUSED
// STANDING row and nothing else, and its URL differs per kind. Rather than
// teach the UI to and-together conditions, the server answers the actual
// question per row and the UI stays a renderer. An absent flag reads as false
// there, so a row carries only the flags for its own kind and every other
// action disappears on its own.
func addSchedulerActionFlags(m map[string]any, kind string) {
	paused := schedFlag(m, "_paused")
	broken := schedFlag(m, "_broken")
	relinkable := schedFlag(m, "_relinkable")
	schedulable := schedFlag(m, "_schedulable")
	switch kind {
	case schedKindStanding:
		m["_edit_standing"] = true
		m["_run_standing"] = !broken // no live agent to run on a parked row
		m["_pause_standing"] = !paused
		m["_resume_standing"] = paused
		m["_relink_standing"] = relinkable
		m["_move_standing"] = true
		m["_del_standing"] = true
	case schedKindRecurring:
		// A parked payload short-circuits at the top of the fire, so Run now
		// would do nothing; a parked task gets Relink or Resume instead. And
		// there is no pause concept in the scheduler store.
		m["_edit_recurring"] = true
		m["_run_recurring"] = !broken
		m["_relink_recurring"] = relinkable
		m["_resume_recurring"] = broken
		m["_del_recurring"] = true
	case schedKindMonitor:
		// Only poll / http_poll / watch have a check to run on demand — a
		// webhook is push-only — and not on a broken one, which has no
		// dependency left to check.
		// Editing a monitor means editing its poll interval, which a webhook
		// does not have — push-only monitors are not on a clock at all.
		m["_edit_monitor"] = schedulable
		m["_test_monitor"] = schedulable && !broken
		m["_pause_monitor"] = !paused
		m["_resume_monitor"] = paused
		m["_relink_monitor"] = relinkable
		m["_move_monitor"] = true
		m["_del_monitor"] = true
	}
}

// schedFlag reads a hidden boolean off a converted row. Absent means false,
// which is what an omitempty field that was false looks like on the way back.
func schedFlag(m map[string]any, key string) bool {
	v, ok := m[key].(bool)
	return ok && v
}
