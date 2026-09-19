package orchestrate

// One page for every goal in flight.
//
// An objective is a schedule with a completion check — never a fifth trigger
// kind and never its own table (docs/loop-objectives.md). That decision is what
// makes the feature cheap, and it is also why the goals themselves had nowhere
// to be read: they live on three different records, in three different
// consoles, and the only way to answer "what am I still waiting on" was to open
// each one and skip every row without an Until.
//
// So this is a read-only UNION, composed the way the Monitor page composes live
// endpoints. Deliberately not the Monitor page itself: that answers what is
// running NOW, and an objective is a standing intention — mostly not running,
// which is exactly why it goes unnoticed.
//
// Read-only on purpose. Every one of these rows is editable, resumable and
// deletable from the Scheduler, and a second surface with its own copies of
// those buttons is a second set of rules for them. This page answers a
// question; the page that owns the record owns the verbs.

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The sections, in the order the page draws them: what needs the owner first,
// what is still working second, what is finished last. The reverse of the
// Scheduler's ordering, and for the reverse reason — there, most rows are
// healthy and the question is "what runs next"; here a row only exists because
// somebody is waiting on it.
const (
	goalSectionStalled  = "Needs you"
	goalSectionInFlight = "Still working"
	goalSectionMet      = "Met"
)

// consoleGoalRow is one outstanding goal.
//
// The GOAL leads, not the schedule's name. On every other page these records
// are a thing that runs and the goal is a detail of it; here the goal is the
// subject and the schedule is where it happens to live.
type consoleGoalRow struct {
	Goal  string `json:"goal"`
	Where string `json:"where"`
	// Stands is the judge's own words on the last attempt, through the same
	// objectiveStateLabel the other three surfaces render, so a goal reads the
	// same here as it does on its own row.
	Stands string `json:"stands,omitempty"`
	// PartOf names the larger piece of work this goal belongs to, when it has
	// one. The Goals page is where this matters most: it is the surface that
	// asks "what am I still waiting on", and three rows that are one piece of
	// work decomposed read as three problems without it.
	PartOf   string `json:"part_of,omitempty"`
	Attempts string `json:"attempts,omitempty"`
	NextRun  string `json:"next_run,omitempty"`
	State    string `json:"state,omitempty"`

	ID string `json:"_id"`
	// Kind names WHICH surface this goal lives on, and Notes offers the one
	// action this page carries.
	//
	// The read-only rule above still holds: it is about the VERBS that change a
	// record, which stay on the page that owns it. A task's notes are what its
	// runs have worked out so far, which is the same question this page exists
	// to answer, one row down. And it is the Scheduler's action, not a copy of
	// it: one client action, one endpoint, one write path. A second set of
	// rules is what the rule forbids, not a second door to the same one.
	Kind     string `json:"_kind,omitempty"`
	Notes    bool   `json:"_notes,omitempty"`
	Met      bool   `json:"_met,omitempty"`
	Stalled  bool   `json:"_stalled,omitempty"`
	InFlight bool   `json:"_in_flight,omitempty"`
}

// handleConsoleGoals serves the merged view: GET /api/console/goals (+ ?agent=
// to scope it, matching the Scheduler).
func (T *OrchestrateApp) handleConsoleGoals(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	out := []map[string]any{}

	for _, sa := range ListStandingAgents(RootDB, user) {
		if strings.TrimSpace(sa.Until) == "" || !standingAgentOnRailOf(sa, agentID) {
			continue
		}
		row := consoleGoalRow{
			Goal:     sa.Until,
			Where:    "Scheduled agent · " + sa.Name,
			Stands:   objectiveStateLabel(standingObjective(sa)),
			Attempts: goalAttemptLabel(sa.UnmetCount, sa.MaxAttempts),
			ID:       sa.Name,
			PartOf:   taskParentLabel(user, sa.Parent),
			Kind:     schedKindStanding,
			Notes:    true,
		}
		if !sa.NextRun.IsZero() && !sa.Paused && !sa.Broken {
			row.NextRun = sa.NextRun.UTC().Format(rfc3339)
		}
		if sa.Broken {
			row.State = parkedStateLabel(StandingParkCause(sa), sa.BrokenReason)
		} else if lbl := scheduleStopLabel(StandingStopCause(sa), StandingStopNote(sa)); lbl != "" {
			row.State = lbl
		}
		out = append(out, goalRow(row, sa.Broken, objectiveMet(sa.Attempts)))
	}

	for _, rt := range listAgentRecurringTasks(user, agentID) {
		p := rt.Payload
		if strings.TrimSpace(p.Until) == "" {
			continue
		}
		row := consoleGoalRow{
			Goal:  p.Until,
			Where: "Recurring task · " + recurringName(p),
			// objectiveAttemptNumber is which attempt the NEXT fire would be,
			// so the number already spent is one less.
			Stands:   objectiveStateLabel(p.objective()),
			Attempts: goalAttemptLabel(objectiveAttemptNumber(p)-1, p.MaxAttempts),
			NextRun:  rt.RunAt,
			ID:       rt.TaskID,
			PartOf:   taskParentLabel(user, p.Parent),
			Kind:     schedKindRecurring,
			Notes:    true,
		}
		if p.Broken {
			row.State = parkedStateLabel(recurringParkCause(p), p.BrokenReason)
			row.NextRun = "" // parked: the dormant re-check is not a next attempt
		}
		out = append(out, goalRow(row, p.Broken, objectiveMet(p.Attempts)))
	}

	for _, m := range ListEventMonitors(RootDB, user) {
		if strings.TrimSpace(m.Until) == "" {
			continue
		}
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		row := consoleGoalRow{
			Goal:  m.Until,
			Where: "Event monitor · " + m.Name,
			// A monitor's objective is bounded by FIRES, not by attempts: it is
			// woken by a condition rather than by a clock, so "how many tries
			// are left" is how many times it may still fire. FireLabel is what
			// the monitor's own row says, reused rather than re-derived.
			Stands:   objectiveStateLabel(monitorObjective(m)),
			Attempts: m.FireLabel(),
			NextRun:  monitorNextRun(m),
			ID:       m.Name,
			PartOf:   taskParentLabel(user, m.Parent),
			Kind:     schedKindMonitor,
			Notes:    true,
		}
		if m.Broken {
			row.State = "⚠ " + m.StopLabel()
		} else if m.Paused {
			row.State = m.StopLabel()
		}
		out = append(out, goalRow(row, m.Broken, m.StopCause() == MonitorStopMet || objectiveMet(m.Attempts)))
	}

	ui.SortRowsBySection(out, []string{goalSectionStalled, goalSectionInFlight, goalSectionMet})
	writeJSON(w, out)
}

// rfc3339 is the one stamp format every scheduled surface reports a next fire
// in, and the one the merged sort compares as text.
const rfc3339 = "2006-01-02T15:04:05Z07:00"

// goalRow files a row under its section and sets the flags the chips read.
//
// Three states and they are exclusive, which is what makes them sections
// rather than filters over one list: a goal is finished, or it is stuck, or it
// is still going. Stalled wins over met, because a park is the thing to read
// first and a parked objective's last attempt was by definition not met.
func goalRow(row consoleGoalRow, broken, met bool) map[string]any {
	section := goalSectionInFlight
	switch {
	case broken:
		row.Stalled, section = true, goalSectionStalled
	case met:
		row.Met, section = true, goalSectionMet
	default:
		row.InFlight = true
	}
	// The shaping only. This page carries its own _kind and its own filter
	// fields, and none of the Scheduler's verbs: see consoleRow.
	return consoleRow(row, section)
}

// objectiveMet reports whether the last judged attempt met the goal. Newest
// last, matching how every surface appends.
func objectiveMet(attempts []ObjectiveAttempt) bool {
	if len(attempts) == 0 {
		return false
	}
	return attempts[len(attempts)-1].Met
}

// goalAttemptLabel says how far into its allowance an objective is.
//
// An unbounded goal shows what it has spent and no denominator, rather than a
// cap it does not have: "3 attempts" and "3 of 5" are different facts and a
// page that prints the first as the second is telling the owner their goal
// will stop when it will not.
func goalAttemptLabel(used, max int) string {
	if used < 0 {
		used = 0
	}
	if max > 0 {
		return fmt.Sprintf("%d of %d attempts", used, max)
	}
	if used == 1 {
		return "1 attempt"
	}
	return fmt.Sprintf("%d attempts", used)
}

// goalsSearchHint and goalsFilters mirror the Scheduler's controls, because the
// two pages list the same records asked two different questions and an owner
// moving between them should not have to learn a second set of chips.
const goalsSearchHint = "Search goals"

func goalsFilters() []ui.OrchestratorViewFilter {
	return []ui.OrchestratorViewFilter{
		{Label: "Show", Options: []ui.OrchestratorFilterOption{
			{Label: "All"},
			{Label: "Needs you", Field: "_stalled"},
			{Label: "Still working", Field: "_in_flight"},
			{Label: "Met", Field: "_met"},
		}},
	}
}
