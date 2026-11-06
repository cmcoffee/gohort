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
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// Section headings, in the order the page draws them. Clock-driven work first,
// because that is what most people come looking for; the condition-driven
// monitors read as the different kind of thing they are at the bottom.
const (
	schedSectionStanding  = "Scheduled agents"
	schedSectionRecurring = "Recurring tasks"
	schedSectionMonitors  = "Event monitors"
	// Done is what finished: a goal met, an end reached, a fire allowance
	// spent. Last, because nothing in it will run again, and it empties
	// itself (done_schedules.go).
	schedSectionDone = "Done"
)

// handleConsoleScheduler serves the merged view: GET /api/console/scheduler
// (+ ?agent= to scope it, which the Manage menu does and the Fleet menu
// deliberately does not).
func (T *OrchestrateApp) handleConsoleScheduler(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	// An agent's schedules are its whole family's: a parent's card lists what
	// its sub-agents run, and a sub-agent's shows the family it belongs to
	// (agent_family.go). One pass per member, each row once.
	family := schedulerFamily(udb, user, agentID)
	// Where a build's time goes, logged for a slow one: the Scheduler opened
	// in about a second and the first guess at why was wrong.
	start := time.Now()
	ix := newStandingRunIndex(user)
	out := []map[string]any{}
	for _, id := range family {
		for _, row := range consoleAgentRows(user, udb, id, ix) {
			out = appendSchedulerRow(out, row, schedSectionStanding, schedKindStanding)
		}
		for _, row := range consoleRecurringRows(user, id) {
			out = appendSchedulerRow(out, row, schedSectionRecurring, schedKindRecurring)
		}
		for _, row := range consoleMonitorRows(user, id) {
			out = appendSchedulerRow(out, row, schedSectionMonitors, schedKindMonitor)
		}
	}
	rowsDone := time.Now()
	out = uniqueSchedulerRows(out)
	sortSchedulerRows(out)
	// The fleet view is every agent's schedules in one list, so each row says
	// whose it is. An agent's own Scheduler does not need it: its family's rows
	// already say "for sub-agent X" where the work is handed on.
	if agentID == "" {
		names := agentNames(udb, user)
		for _, m := range out {
			if title, ok := m["name"].(string); ok {
				id, _ := m["_agent"].(string)
				m["name"] = agentRowLabel(names, id, title)
			}
		}
	}
	// Link groups lead, each drawn as one box, and their members are not
	// listed a second time in their own sections. Added after the sort, which
	// orders rows by their own next fire and would split a group apart.
	var groups []map[string]any
	placed := map[string]bool{}
	for _, id := range family {
		g, p := appendLinkGroupRows(nil, user, udb, id, ix)
		groups = append(groups, g...)
		for k, v := range p {
			placed[k] = placed[k] || v
		}
	}
	groups = uniqueSchedulerRows(groups)
	if len(groups) > 0 {
		rest := out[:0:0]
		for _, m := range out {
			if !placed[linkRowRef(m)] {
				rest = append(rest, m)
			}
		}
		out = append(groups, rest...)
	}
	addLastRunBadges(out, ix)
	if took := time.Since(start); took > 300*time.Millisecond {
		Debug("[console.scheduler] agent=%q family=%d rows=%d took %s (rows %s, ledger %d runs, groups %s)",
			agentID, len(family), len(out), took.Round(time.Millisecond), rowsDone.Sub(start).Round(time.Millisecond),
			len(ix.runs), time.Since(rowsDone).Round(time.Millisecond))
	}
	return out, nil
}

// addLastRunBadges gives each schedule a badge saying how its latest run went
// (ui: "_status", "_status_tone"), from the one ledger read the page already
// makes. It answers "how did the last one go", where the warning marker
// answers "does it keep going wrong": one good run in a string of bad ones
// reads OK here and stays marked there.
func addLastRunBadges(rows []map[string]any, ix *standingRunIndex) {
	for _, m := range rows {
		id := schedStr(m, "_id")
		var f RunFilter
		switch m["_kind"] {
		case schedKindStanding:
			f = RunFilter{}.AboutStanding(id)
		case schedKindRecurring:
			f = RunFilter{Task: schedStr(m, "_task")}.AboutAgent(schedStr(m, "_agent"), "")
		case schedKindMonitor:
			f = RunFilter{}.AboutMonitor(id)
		default:
			continue
		}
		if id == "" {
			continue
		}
		label, tone := lastRunBadge(ix.latestFor(f))
		m["_status"], m["_status_tone"] = label, tone
		// The standing row's own cell said the same in grey.
		delete(m, "status")
	}
}

// lastRunBadge is a latest run status as the badge says it, and its tone.
func lastRunBadge(status string) (label, tone string) {
	switch RunStatus(status) {
	case RunOK:
		return "OK", "ok"
	case RunAttention:
		return "ATTENTION", "warn"
	case RunFailed:
		return "FAILED", "bad"
	case RunRunning:
		return "RUNNING", ""
	case "":
		return "NOT RUN YET", ""
	}
	return strings.ToUpper(status), ""
}

// agentRowLabel is a fleet Scheduler row's title, led by the name of the agent
// it belongs to. The list held every agent's schedules under titles like
// "Daily digest", with nothing to say which agent would run one.
func agentRowLabel(names map[string]string, agentID, title string) string {
	return "(" + firstNonEmptyStr(names[agentID], agentID, "no agent") + ") " + title
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
	ui.SortRowsBySection(rows, []string{schedSectionStanding, schedSectionRecurring, schedSectionMonitors, schedSectionDone})
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
	// A finished schedule is filed under Done whatever kind it is, with Keep
	// (or its undo) to hold it past the cleanup.
	if schedFlag(m, "_done") {
		m["_section"] = schedSectionDone
		m["_keep"] = !schedFlag(m, "_kept")
		m["_unkeep"] = schedFlag(m, "_kept")
	}
	// The kind-qualified id the parent and roll-up actions address a row by
	// (their IDField): _id alone is a bare name, and a monitor and a standing
	// agent can share one.
	if id, ok := m["_id"].(string); ok && id != "" {
		m["_ref"] = kind + ":" + id
	}
	addSchedulerActionFlags(m, kind)
	addSchedulerFilterFlags(m)
	return m
}

// handleConsoleSchedulerAttention is the schedules that need attention, one row
// each, for the nav badge. Built from the schedule records alone: the full
// list reads the run ledger for every standing agent and writes missing
// mission summaries, and doing that whenever a session changed is what made
// the Scheduler slow to open (scheduler_load_test.go). Attention needs none of
// it: parked, and the failure and guardrail streaks, are on the records.
func (T *OrchestrateApp) handleConsoleSchedulerAttention(c *jsonapi.Call) (any, error) {
	user, udb := c.User, c.UDB
	family := schedulerFamily(udb, user, strings.TrimSpace(c.R.URL.Query().Get("agent")))
	out := []map[string]any{}
	seen := map[string]bool{}
	add := func(kind, id string, m map[string]any) {
		addSchedulerFilterFlags(m)
		if !schedFlag(m, "_attention") || seen[kind+":"+id] {
			return
		}
		seen[kind+":"+id] = true
		out = append(out, map[string]any{"name": id, "_attention": true, "_kind": kind})
	}
	for _, id := range family {
		for _, sa := range ListStandingAgents(RootDB, user) {
			if standingAgentOnRailOf(sa, id) {
				add(schedKindStanding, sa.Name, map[string]any{"_broken": sa.Broken, "_fail_streak": float64(sa.ConsecutiveFailures)})
			}
		}
		for _, row := range consoleRecurringRows(user, id) {
			add(schedKindRecurring, row.ID, map[string]any{"_broken": row.Broken, "_fail_streak": float64(row.FailStreak), "_guard_streak": float64(row.GuardStreak)})
		}
		for _, row := range consoleMonitorRows(user, id) {
			add(schedKindMonitor, row.ID, map[string]any{"_broken": row.Broken, "_fail_streak": float64(row.FailStreak)})
		}
	}
	return out, nil
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
	// Needs attention: parked, or failing or being stopped by a guardrail
	// again and again. Once is not "again": one failed fire is usually a
	// blip, and one stop is usually the guardrail doing its job.
	failing := schedInt(m, "_fail_streak") >= schedAttentionStreak
	guarded := schedInt(m, "_guard_streak") >= schedAttentionStreak
	m["_attention"] = broken || failing || guarded
	// The row's warning marker (ui: "_warn", "_warn_why"), saying why in the
	// row's own words: the parked state, the failing streak, the guardrail.
	if broken || failing || guarded {
		m["_warn"] = true
		var why []string
		if broken {
			why = append(why, strings.TrimSpace(strings.TrimPrefix(schedStr(m, "state"), "⚠")))
		}
		if failing {
			why = append(why, schedStr(m, "failing"))
		}
		if guarded {
			why = append(why, schedStr(m, "guarded"))
		}
		m["_warn_why"] = strings.Join(nonBlank(why), "; ")
		// Said once: the warning line carries these, so their own cells go
		// rather than repeat it in grey beside it.
		if broken {
			delete(m, "state")
		}
		if failing {
			delete(m, "failing")
		}
		if guarded {
			delete(m, "guarded")
		}
	}
	// At rest: not going to fire again until somebody does something: paused
	// (any kind) or parked.
	m["_at_rest"] = broken || schedFlag(m, "_paused")
}

// schedAttentionStreak is how many failures, or guardrail stops, in a row it
// takes for a schedule to need attention.
const schedAttentionStreak = 2

// schedInt reads a count off a row map (JSON numbers arrive as float64).
func schedInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// schedStr reads a text field off a row map.
func schedStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// nonBlank drops empty strings.
func nonBlank(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
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
	// Linking is offered on every clock-driven row that can run.
	m["_link"] = (kind == schedKindStanding || kind == schedKindRecurring) && !broken
	switch kind {
	case schedKindStanding:
		m["_edit_standing"] = true
		m["_run_standing"] = !broken // no live agent to run on a parked row
		m["_pause_standing"] = !paused
		m["_resume_standing"] = paused
		m["_relink_standing"] = relinkable
		m["_del_standing"] = true
	case schedKindRecurring:
		// A parked payload short-circuits at the top of the fire, so Run now
		// would do nothing; a parked task gets Relink or Resume instead. A
		// paused one gets its own Resume, without the parked one's warning.
		m["_edit_recurring"] = true
		m["_run_recurring"] = !broken
		m["_relink_recurring"] = relinkable
		m["_pause_recurring"] = !broken && !paused
		m["_resume_recurring"] = broken
		m["_unpause_recurring"] = paused && !broken
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
			{Label: "Done", Field: "_section", Equals: schedSectionDone},
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
