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
	"github.com/cmcoffee/gohort/core/ui"
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
	sortSchedulerRows(out)
	writeJSON(w, out)
}

// sortSchedulerRows puts each section in the order it will happen.
//
// The page answers "what is this going to do on its own", and a list in store
// order answers it for one row at a time: to find out what fires next you read
// every date on the page. Soonest first, then the ones with no next fire at all
// — paused, parked, push-triggered — because the things that WILL happen, in
// the order they will happen, and then the things that will not, is how a
// schedule reads out loud.
//
// Sorting only WITHIN a section: the cards layout draws a heading each time
// _section changes, so a global sort by time would scatter the three kinds and
// redraw the headings on nearly every row. The section's own position leads the
// comparison rather than being a special case inside it — a comparator that
// calls two rows from different sections "equal" is not an ordering at all, and
// a sort given one is entitled to produce anything.
//
// Ties break on the name, so the order is stable across refreshes rather than
// shuffling rows somebody is aiming at.
//
// Every kind reports its next fire under next_run, in RFC3339 UTC, which sorts
// correctly as text — a fixed offset and a fixed width being the whole point of
// that format.
// sortSchedulerRows puts each section of the Scheduler in the order it will
// happen. The ORDERING is ui.SortRowsBySection — the "_section" convention is
// core/ui's, so the sorter that serves it lives there. What belongs to this app
// is the order of the sections themselves.
func sortSchedulerRows(rows []map[string]any) {
	ui.SortRowsBySection(rows, []string{schedSectionStanding, schedSectionRecurring, schedSectionMonitors})
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

// consoleRow is the SHAPING half, and it is all the Goals page wants: a typed
// console row as its own JSON, plus the section it files under.
//
// Through JSON rather than a hand-written field copy, so a merged view renders
// EXACTLY what the single view renders, including the omitempty rules, and a
// field added to one of the row types appears without anybody remembering to
// add it here.
//
// Split from the flagging below because two pages compose these rows and only
// one of them wants the Scheduler's verbs. That used to be expressed by passing
// an empty kind, which is a sentinel doing the job of a function boundary: it
// silently put an empty _kind over the one the Goals page had set, and a row
// carrying an id whose surface nobody can name looks entirely normal in the
// JSON. A caller now asks for what it wants by name.
func consoleRow(row any, section string) map[string]any {
	b, err := json.Marshal(row)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	m["_section"] = section
	return m
}

// schedulerRow is the shaping plus everything that makes a row ACTIONABLE on
// the Scheduler: which surface it belongs to, and one flag per button.
//
// _kind is here rather than on the three row types because it answers a
// question only this page's actions ask: the notes action has one URL for all
// three kinds and a different target per kind, and a name shared by a monitor
// and a standing agent would otherwise resolve to whichever the server guessed.
// Every other action gates on a per-kind flag, which an absent field answers
// for free.
func schedulerRow(row any, section, kind string) map[string]any {
	m := consoleRow(row, section)
	if m == nil {
		return nil
	}
	m["_kind"] = kind
	m["_notes"] = true
	addSchedulerActionFlags(m, kind)
	addSchedulerFilterFlags(m)
	return m
}

// addSchedulerFilterFlags composes the two questions the page's filter chips
// ask, into one field each.
//
// A chip tests ONE field (see ui.OrchestratorFilterOption, deliberately: a
// filter vocabulary that can and-or fields is a query language, and core/ui
// would then need to learn what a schedule is). So the joining happens here,
// where the answer is already known, exactly as the row's action flags do.
//
// Both are composed rather than reused from an existing field because neither
// question maps onto one. A parked row needs attention whether or not its
// checks have been failing, and a row that has spent its fires is at rest
// without anything being wrong with it.
func addSchedulerFilterFlags(m map[string]any) {
	broken := schedFlag(m, "_broken")
	// Failing carries the streak, and is empty while nothing is wrong.
	failing, _ := m["failing"].(string)
	m["_attention"] = broken || failing != ""
	// At rest: not going to fire again until somebody does something. Paused is
	// how a standing agent and a monitor say it; a recurring task has no pause
	// in the scheduler store, so parked is how it says the same thing.
	m["_at_rest"] = broken || schedFlag(m, "_paused")
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
		//
		// Edit is offered on EVERY monitor, including the push-triggered one.
		// It used to be gated on schedulable because editing meant editing an
		// interval, and a webhook has none; now it also edits what the monitor
		// watches for and what it tells the agent when it fires, which a
		// webhook has exactly like the rest. The modal's timing half is the
		// part that says "push-triggered, nothing to time here".
		m["_edit_monitor"] = true
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

// schedulerSearchHint is the search box's placeholder. It says SCHEDULES
// rather than "search", because the box sits on a page that also has an agent
// picker and a thread list, and a bare magnifier is a promise about scope that
// nothing on screen keeps.
const schedulerSearchHint = "Search schedules"

// schedulerFilters are the chips above the list.
//
// Two questions, because they are the two an owner actually arrives with.
// "Show me the monitors" is about which KIND, and answers a page whose three
// sections have grown past one screen. "Show me what needs me" is about STATE,
// and is the reason a list of everything that runs on a clock is worth having
// at all: a schedule is something you stop reading once it works, so the only
// way back in is the page telling you which ones stopped working.
//
// The first option of each group is the default and the one that hides nothing.
// Every chip carries the count it would leave on screen, so "Needs attention 0"
// answers the question without being clicked, which is the state it is in on
// almost every day.
func schedulerFilters() []ui.OrchestratorViewFilter {
	return []ui.OrchestratorViewFilter{
		{Label: "Kind", Options: []ui.OrchestratorFilterOption{
			{Label: "All"},
			{Label: "Agents", Field: "_section", Equals: schedSectionStanding},
			{Label: "Tasks", Field: "_section", Equals: schedSectionRecurring},
			{Label: "Monitors", Field: "_section", Equals: schedSectionMonitors},
		}},
		{Label: "Show", Options: []ui.OrchestratorFilterOption{
			{Label: "Everything"},
			// Composed server-side; see addSchedulerFilterFlags.
			{Label: "Needs attention", Field: "_attention"},
			{Label: "At rest", Field: "_at_rest"},
			// Straight off the visible column, no composition needed: a row
			// with an objective has text there and a row without has none.
			{Label: "With a goal", Field: "objective"},
		}},
	}
}
