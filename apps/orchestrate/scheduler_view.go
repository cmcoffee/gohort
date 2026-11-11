package orchestrate

// View on a Scheduler row: one item's overview, read only. How it has been
// going (its last runs, or a monitor's last checks, said True, False or Issue
// as the card says them) and what its runs have left each other (its notes,
// with Edit beside them). It replaced two buttons: Notes, which opened the
// notes as an editor, the rare thing to want, and Recent checks on monitors.
// The card's own fields (state, schedule, next run, goal) are drawn from the
// row by the page; this serves what the row does not carry.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// schedulerViewRuns is how many of an item's runs, and a monitor's checks, a
// View shows.
const schedulerViewRuns = 8

// schedulerViewSummaryMax bounds one run's output in a View. It is the run's
// whole report, so it is generous; the page folds it.
const schedulerViewSummaryMax = 8000

type schedulerViewRun struct {
	ID      string `json:"id,omitempty"` // the run's ledger id, for a tool that reads one whole
	When    string `json:"when"`
	Status  string `json:"status"`
	Trigger string `json:"trigger,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type schedulerViewCheck struct {
	When    string `json:"when"`
	Verdict string `json:"verdict,omitempty"` // True, False or Issue, for an agent-judged monitor
	Note    string `json:"note,omitempty"`
}

// handleConsoleSchedulerView is GET /api/console/scheduler/view?kind=&id=,
// the caller's own item only (the same lookup the notes use).
func (T *OrchestrateApp) handleConsoleSchedulerView(c *jsonapi.Call) (any, error) {
	q := c.R.URL.Query()
	out, ok := schedulerViewFor(c.User, q.Get("kind"), q.Get("id"))
	if !ok {
		return nil, errNotFound
	}
	return out, nil
}

// schedulerViewFor is the View of user's item kind/id, and false when user
// has no such item.
func schedulerViewFor(user, kind, id string) (map[string]any, bool) {
	kind, id = strings.TrimSpace(kind), strings.TrimSpace(id)
	if user == "" || id == "" {
		return nil, false
	}
	loc := UserLocation(user)
	out := map[string]any{"kind": kind}
	filter, runs := RunFilter{}, true
	switch kind {
	case schedKindStanding:
		sa, ok := GetStandingAgent(RootDB, user, id)
		if !ok {
			return nil, false
		}
		filter = filter.AboutStanding(sa.Name)
	case schedKindRecurring:
		found := false
		for _, rt := range listAgentRecurringTasks(user, "") {
			if rt.TaskID == id {
				filter.Task, found = recurringName(rt.Payload), true
				break
			}
		}
		if !found {
			return nil, false
		}
	case schedKindMonitor:
		m, ok := GetEventMonitor(RootDB, user, id)
		if !ok {
			return nil, false
		}
		filter = filter.AboutMonitor(m.Name)
		out["checks"] = schedulerViewChecks(m)
	case schedKindLink:
		if _, ok := getLinkGroup(user, id); !ok {
			return nil, false
		}
		runs = false // a group's runs are its members'
	default:
		return nil, false
	}
	if runs {
		filter.Limit = schedulerViewRuns
		rows := []schedulerViewRun{}
		for _, rec := range ListRuns(RootDB, user, filter) {
			// What the run said in full: its whole output where the ledger
			// kept one (the summary is a short feed line, cut at 280), else
			// the summary; a failure's error over either.
			summary := strings.TrimSpace(rec.Summary)
			if full, ok := GetRun(RootDB, user, rec.ID); ok && strings.TrimSpace(full.Raw) != "" {
				summary = strings.TrimSpace(full.Raw)
			}
			if rec.Status == RunFailed && strings.TrimSpace(rec.Err) != "" {
				summary = strings.TrimSpace(rec.Err)
			}
			// Whole, up to a bound on the payload: the View folds a long one
			// with "more", and a cut here left the rest unreadable.
			summary = truncateObs(summary, schedulerViewSummaryMax)
			rows = append(rows, schedulerViewRun{ID: rec.ID, When: consoleRunWhen(rec, loc), Status: string(rec.Status), Trigger: rec.Trigger, Summary: summary})
		}
		out["runs"] = rows
	}
	if tn, ok := taskNotesFor(user, kind, id); ok {
		n := tn.load()
		notes := map[string]any{"text": n.Text}
		if !n.UpdatedAt.IsZero() {
			notes["updated"] = n.UpdatedAt.In(loc).Format("Jan 2 3:04 PM")
		}
		out["notes"] = notes
	}
	return out, true
}

// schedulerViewChecks is a monitor's recent checks, newest first. An
// agent-judged monitor's are said as the card says its last one, True, False
// or Issue, by the rule the check fires by (PollVerdict); another kind's keep
// their outcome as recorded.
func schedulerViewChecks(m EventMonitor) []schedulerViewCheck {
	out := []schedulerViewCheck{}
	for i := len(m.RecentChecks) - 1; i >= 0 && len(out) < schedulerViewRuns; i-- {
		ck := m.RecentChecks[i]
		row := schedulerViewCheck{When: ck.At.Local().Format("Jan 2 3:04 PM"), Note: strings.TrimSpace(ck.Outcome)}
		if d := strings.TrimSpace(ck.Detail); d != "" {
			row.Note += ": " + d
		}
		if m.Kind == EventKindPoll {
			row.Verdict, row.Note = pollCheckVerdict(m, ck.Outcome, ck.Detail)
		}
		out = append(out, row)
	}
	return out
}

// pollCheckVerdict reads one recorded check of an agent-judged monitor. A
// failed check is an Issue saying why. Otherwise the checker's answer follows
// the recorded outcome's first ": " (`answer lacks "YES": <answer>`), and is
// read as the last check is (PollVerdict), so a check and the card agree.
func pollCheckVerdict(m EventMonitor, outcome, detail string) (verdict, note string) {
	answer := ""
	if i := strings.Index(detail, ": "); i >= 0 {
		answer = strings.TrimSpace(detail[i+2:])
	}
	switch strings.TrimSpace(outcome) {
	case "failed":
		return PollIssue, "the check failed: " + strings.TrimSpace(detail)
	case "held":
		// Still so since it fired: true, whatever words it was said in.
		return PollTrue, answer
	}
	if answer == "" {
		answer = detail
	}
	if v, n := PollVerdict(EventMonitor{Kind: EventKindPoll, LastResult: answer, MatchContains: m.MatchContains}); v != "" {
		return v, n
	}
	return "", strings.TrimSpace(outcome + ": " + detail)
}
