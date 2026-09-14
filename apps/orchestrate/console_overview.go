package orchestrate

// The two overview panes.
//
// The Manage menu grew by accretion, and what it accreted was two different
// questions wearing one hat: what is THIS agent doing, and what is my whole
// fleet doing on my behalf. Every entry looked scoped to the agent in view
// because the menu sits in that agent's topbar and every source is fetched
// with its id — so the fleet-wide ones quietly misread as the agent's own.
//
// These two handlers answer the two questions directly, each by gathering
// what the existing panes already know. They own no data: every number here
// is read through the same helper the detailed pane uses, so a figure in the
// summary and the list behind it cannot disagree.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// overviewCard is one card in either pane. The card renderer shows values,
// not keys, in field order: Title is the bold headline, Status renders as a
// pill, and the rest are muted detail. "_section" groups cards under a
// heading; "_run" marks the rows a Details button applies to.
//
// WHAT GOES IN Title DEPENDS ON THE PANE, and the rule is the same one the
// fleet-wide panes follow: in a fleet view the AGENT leads, because the list
// is drawn from every agent the owner has and the first question about any row
// in it is whose. The specific thing — the rule that blocked, the schedule
// that stopped — follows on the detail line. In the per-agent view the agent
// is already known, so naming it on every row says nothing, and the specific
// thing leads instead.
type overviewCard struct {
	Title   string `json:"title"`
	Status  string `json:"Status,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Extra   string `json:"extra,omitempty"`
	Section string `json:"_section"`
	Run     bool   `json:"_run,omitempty"`
	ID      string `json:"_id,omitempty"`
}

// Section headings. Named once so the two panes agree on what a section is
// called and a heading cannot drift between them.
const (
	secGlance    = "At a glance"
	secRuns      = "Recent runs"
	secStanding  = "Standing work"
	secAttention = "Needs attention"
	secSpend     = "Spend by agent"
)

// overviewRunLimit bounds the ledger scan behind both panes. Runs are read
// newest-first, so this is a window on recent history rather than a sample of
// all of it; the Runs pane is where the full record lives.
const overviewRunLimit = 300

// handleConsoleOverview answers "what is THIS agent doing" — the per-agent
// summary. Everything is scoped to the agent in view, including the runs its
// schedules and monitors produced, which is what makes it different from the
// fleet pane rather than a filtered copy of it.
func (T *OrchestrateApp) handleConsoleOverview(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agentID == "" {
		writeJSON(w, []overviewCard{{Title: "No agent selected", Detail: "Open an agent to see what it has been doing.", Section: secGlance}})
		return
	}
	loc := UserLocation(user)
	now := time.Now()
	name := agentID
	for _, a := range listAgents(udb, user) {
		if a.ID == agentID {
			name = chFirst(a.Name, a.ID)
			break
		}
	}

	runs := agentRuns(user, udb, agentID, name)
	standing, monitors, recurring := agentStandingWork(user, agentID)
	blocks := listGuardrailBlocks(udb, agentID, 5)

	cards := []overviewCard{}

	// At a glance. Each card is a number and what it measures, because the
	// number is what the eye is here for.
	cards = append(cards, overviewCard{
		Title:   runCountLabel(runs, now),
		Status:  runHealthStatus(runs, now),
		Detail:  lastRunLabel(runs, loc),
		Section: secGlance,
	})
	if spend := agentSpendFor(user, agentID, now, loc); spend != nil {
		cards = append(cards, overviewCard{
			Title:   spend.Month,
			Detail:  "over 30 days · " + spend.Week + " this week",
			Extra:   spend.Runs,
			Section: secGlance,
		})
	}
	cards = append(cards, overviewCard{
		Title:   standingWorkLabel(len(standing), len(monitors), len(recurring)),
		Detail:  "scheduled and event-driven work that runs without you",
		Section: secGlance,
	})

	// Recent runs, with the same Details modal the Runs pane opens.
	for _, rec := range topRuns(runs, 6) {
		cards = append(cards, overviewCard{
			Title:   consoleRunTitle(rec),
			Status:  string(rec.Status),
			Detail:  consoleRunWhen(rec, loc),
			Extra:   truncateObs(firstNonEmpty(rec.Summary, rec.Err), 140),
			Section: secRuns,
			Run:     true,
			ID:      rec.ID,
		})
	}

	// Standing work, one line each — what it is, when it runs, whether it is
	// actually running.
	for _, sa := range standing {
		state := "active"
		if lbl := scheduleStopLabel(StandingStopCause(sa), StandingStopNote(sa)); lbl != "" {
			state = lbl
		} else if sa.Paused {
			state = "paused"
		}
		cards = append(cards, overviewCard{
			Title: sa.Name, Status: state, Detail: StandingScheduleLabel(sa),
			Extra: truncateObs(sa.Mission, 120), Section: secStanding,
		})
	}
	for _, m := range monitors {
		state := "active"
		if m.Paused {
			state = "paused"
		}
		cards = append(cards, overviewCard{
			Title: m.Name, Status: state, Detail: "event monitor", Section: secStanding,
		})
	}
	for _, rt := range recurring {
		label := firstLineLabel(rt.Payload.Prompt)
		if label == "" {
			label = "recurring task"
		}
		state := "active"
		if rt.Payload.Broken {
			state = "parked"
		}
		cards = append(cards, overviewCard{
			Title: label, Status: state, Detail: recurringDetail(rt.Payload),
			Extra: fmt.Sprintf("%d fired", rt.Payload.FireCount), Section: secStanding,
		})
	}

	// What is wrong, if anything: the blocks this agent's rules produced, and
	// the failed runs worth a second look.
	for _, b := range blocks {
		cards = append(cards, overviewCard{
			Title: guardrailRowTitle(b.Rule), Status: "blocked",
			Detail: guardrailRowWhere(b), Extra: truncateObs(b.Reason, 140),
			Section: secAttention,
		})
	}
	for _, rec := range failedRuns(runs, now, 3) {
		cards = append(cards, overviewCard{
			Title: consoleRunTitle(rec), Status: string(rec.Status),
			Detail: consoleRunWhen(rec, loc), Extra: truncateObs(firstNonEmpty(rec.Err, rec.Summary), 140),
			Section: secAttention, Run: true, ID: rec.ID,
		})
	}

	writeJSON(w, cards)
}

// handleConsoleFleet answers "what is my whole fleet doing on my behalf".
// Deliberately takes no agent: the menu marks this item fleet-scoped so the
// selected agent is not appended, and nothing here narrows by one.
func (T *OrchestrateApp) handleConsoleFleet(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	loc := UserLocation(user)
	now := time.Now()
	runs := ListRuns(RootDB, user, RunFilter{Limit: overviewRunLimit})
	spend := agentSpendRows(user, now, loc)
	broken := brokenToolActions(toolOutcomeStore(), user)
	names := agentNames(udb, user)

	cards := []overviewCard{}

	// At a glance: the four things worth knowing before looking at anything.
	cards = append(cards, overviewCard{
		Title:   runCountLabel(runs, now),
		Status:  runHealthStatus(runs, now),
		Detail:  lastRunLabel(runs, loc),
		Section: secGlance,
	})
	if total := fleetSpendLabel(spend); total != "" {
		cards = append(cards, overviewCard{
			Title:   total,
			Detail:  fmt.Sprintf("over 30 days, across %d agent(s)", len(spend)),
			Section: secGlance,
		})
	}
	standing, monitors, recurring := agentStandingWork(user, "")
	cards = append(cards, overviewCard{
		Title:   standingWorkLabel(len(standing), len(monitors), len(recurring)),
		Detail:  "scheduled and event-driven work across every agent",
		Section: secGlance,
	})
	if parked := parkedCount(standing, recurring); parked > 0 {
		cards = append(cards, overviewCard{
			Title:   fmt.Sprintf("%d parked", parked),
			Status:  "attention",
			Detail:  "schedules stopped and waiting on you",
			Section: secGlance,
		})
	}

	// Where the money goes. Top few only; the Spend pane has the rest.
	for i, row := range spend {
		if i >= 6 {
			break
		}
		cards = append(cards, overviewCard{
			Title: row.Agent, Detail: row.Month + " over 30 days",
			Extra: row.Runs + " · last " + chFirst(row.Last, "never"), Section: secSpend,
		})
	}

	for _, rec := range topRuns(runs, 8) {
		cards = append(cards, overviewCard{
			Title: consoleRunAgent(rec), Status: string(rec.Status),
			Detail:  joinDetail(consoleRunTask(rec), consoleRunWhen(rec, loc)),
			Extra:   truncateObs(firstNonEmpty(rec.Summary, rec.Err), 140),
			Section: secRuns, Run: true, ID: rec.ID,
		})
	}

	// Needs attention, worst first: tools that never work, then schedules that
	// have stopped, then the runs that failed.
	for i, rec := range broken {
		if i >= 5 {
			break
		}
		cards = append(cards, overviewCard{
			Title: rec.Tool + " · " + rec.Action, Status: "broken",
			Detail:  fmt.Sprintf("%d failure(s), never succeeded", rec.Fail),
			Extra:   truncateObs(rec.LastError, 140),
			Section: secAttention,
		})
	}
	for _, sa := range standing {
		lbl := scheduleStopLabel(StandingStopCause(sa), StandingStopNote(sa))
		if lbl == "" {
			continue
		}
		// The agent leads where there is one. A schedule that runs a pipeline
		// or a machine has no agent at all, so there the schedule's own name
		// is the most specific thing it has and it leads instead.
		title, detail := sa.Name, StandingScheduleLabel(sa)
		if who := names[chFirst(sa.AgentID, sa.ReportAgentID)]; who != "" {
			title, detail = who, joinDetail(sa.Name, StandingScheduleLabel(sa))
		}
		cards = append(cards, overviewCard{
			Title: title, Status: lbl, Detail: detail,
			Extra: truncateObs(sa.Mission, 120), Section: secAttention,
		})
	}
	for _, rec := range failedRuns(runs, now, 4) {
		cards = append(cards, overviewCard{
			Title: consoleRunTitle(rec), Status: string(rec.Status),
			Detail: consoleRunWhen(rec, loc), Extra: truncateObs(firstNonEmpty(rec.Err, rec.Summary), 140),
			Section: secAttention, Run: true, ID: rec.ID,
		})
	}
	// Blocks last: a block is the rules working, not a fault, so it reports
	// without claiming the top of the list.
	for _, e := range fleetGuardrailBlocks(udb, user, 4) {
		cards = append(cards, overviewCard{
			Title: chFirst(e.agent.Name, e.agent.ID), Status: "blocked",
			Detail: joinDetail(guardrailRowTitle(e.block.Rule), guardrailRowWhere(e.block)),
			Extra:  truncateObs(e.block.Reason, 140), Section: secAttention,
		})
	}

	if len(cards) == 0 {
		cards = append(cards, overviewCard{Title: "Nothing has run yet", Detail: "Schedules, monitors and dispatched work show up here once they do.", Section: secGlance})
	}
	writeJSON(w, cards)
}

// --- gathering ---------------------------------------------------------------

// agentRuns returns the runs that belong to one agent: its own, plus the ones
// its schedules, monitors and recurring tasks produced. A run is filed under
// the thing that fired it, so asking only for "agent:<id>" answers with the
// chat turns and none of the work the agent actually has standing.
func agentRuns(user string, udb Database, agentID, name string) []RunRecord {
	subjects := map[string]bool{(RunRecord{}).AboutAgent(agentID).Subject: true}
	standing, monitors, recurring := agentStandingWork(user, agentID)
	for _, sa := range standing {
		subjects[(RunRecord{}).AboutStanding(sa.Name).Subject] = true
	}
	for _, m := range monitors {
		subjects[(RunRecord{}).AboutMonitor(m.Name).Subject] = true
	}
	for _, rt := range recurring {
		subjects[(RunRecord{}).AboutTrigger(rt.TaskID).Subject] = true
	}
	var out []RunRecord
	for _, rec := range ListRuns(RootDB, user, RunFilter{Limit: overviewRunLimit}) {
		switch {
		case rec.Subject != "" && subjects[rec.Subject]:
		// A record written before subjects existed carries only a display
		// label. Matching it by name is as imprecise as it always was, and
		// dropping it would make an agent's history start at the upgrade.
		case rec.Subject == "" && (rec.Agent == agentID || rec.Agent == name):
		default:
			continue
		}
		out = append(out, rec)
	}
	return out
}

// agentStandingWork returns the standing agents, event monitors and recurring
// tasks belonging to one agent, or to the whole fleet when agentID is empty.
// Each list is gathered the same way its own console pane gathers it, so the
// overview counts what the pane would show.
func agentStandingWork(user, agentID string) ([]StandingAgent, []EventMonitor, []recurringTaskRow) {
	var standing []StandingAgent
	for _, sa := range ListStandingAgents(RootDB, user) {
		if standingAgentOnRailOf(sa, agentID) {
			standing = append(standing, sa)
		}
	}
	var monitors []EventMonitor
	for _, m := range ListEventMonitors(RootDB, user) {
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		monitors = append(monitors, m)
	}
	return standing, monitors, listAgentRecurringTasks(user, agentID)
}

// fleetGuardrailEntry pairs a block with the agent whose rule produced it.
type fleetGuardrailEntry struct {
	block GuardrailBlock
	agent AgentRecord
}

// fleetGuardrailBlocks returns the most recent blocks across every agent.
func fleetGuardrailBlocks(udb Database, user string, limit int) []fleetGuardrailEntry {
	var all []fleetGuardrailEntry
	for _, a := range listAgents(udb, user) {
		for _, b := range listGuardrailBlocks(udb, a.ID, limit) {
			all = append(all, fleetGuardrailEntry{block: b, agent: a})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].block.At.After(all[j].block.At) })
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// agentSpendFor picks one agent's row out of the fleet aggregation, so the
// summary and the Spend pane are reading the same arithmetic.
func agentSpendFor(user, agentID string, now time.Time, loc *time.Location) *agentSpendRow {
	for _, row := range agentSpendRows(user, now, loc) {
		if row.ID == agentID {
			r := row
			return &r
		}
	}
	return nil
}

// agentNames maps agent id to the name a person would recognise, for the fleet
// pane's rows that lead with whose they are.
func agentNames(udb Database, user string) map[string]string {
	out := map[string]string{}
	for _, a := range listAgents(udb, user) {
		out[a.ID] = chFirst(a.Name, a.ID)
	}
	return out
}

// joinDetail joins the parts of a detail line, skipping the empty ones so a
// row missing one does not render a stray separator.
func joinDetail(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// --- labels ------------------------------------------------------------------

// topRuns returns the first n runs (the ledger hands them back newest first).
func topRuns(runs []RunRecord, n int) []RunRecord {
	if len(runs) > n {
		return runs[:n]
	}
	return runs
}

// failedRuns returns recent runs that ended badly, newest first.
func failedRuns(runs []RunRecord, now time.Time, n int) []RunRecord {
	cutoff := now.AddDate(0, 0, -7)
	var out []RunRecord
	for _, rec := range runs {
		if rec.Status != RunFailed || rec.Started.Before(cutoff) {
			continue
		}
		out = append(out, rec)
		if len(out) >= n {
			break
		}
	}
	return out
}

// runCountLabel counts today and the past week, which is the pair that says
// whether anything is happening and whether that is normal.
func runCountLabel(runs []RunRecord, now time.Time) string {
	day := now.AddDate(0, 0, -1)
	week := now.AddDate(0, 0, -7)
	today, seven := 0, 0
	for _, rec := range runs {
		if rec.Started.After(week) {
			seven++
		}
		if rec.Started.After(day) {
			today++
		}
	}
	if seven == 0 {
		return "No runs this week"
	}
	return fmt.Sprintf("%d run(s) in 24h · %d in 7 days", today, seven)
}

// runHealthStatus renders as the card's pill: whether the recent week holds
// failures worth looking at.
func runHealthStatus(runs []RunRecord, now time.Time) string {
	week := now.AddDate(0, 0, -7)
	failed, running := 0, 0
	for _, rec := range runs {
		if rec.Started.Before(week) {
			continue
		}
		switch rec.Status {
		case RunFailed:
			failed++
		case RunRunning:
			running++
		}
	}
	switch {
	case running > 0:
		return fmt.Sprintf("%d running", running)
	case failed > 0:
		return fmt.Sprintf("%d failed", failed)
	}
	return ""
}

// lastRunLabel says when anything last ran, in the owner's own zone.
func lastRunLabel(runs []RunRecord, loc *time.Location) string {
	if len(runs) == 0 {
		return "nothing has run yet"
	}
	return "last run " + runs[0].Started.In(loc).Format("Jan 2 15:04")
}

// standingWorkLabel counts the three kinds of standing work in one phrase,
// naming only the kinds that exist.
func standingWorkLabel(standing, monitors, recurring int) string {
	var parts []string
	if standing > 0 {
		parts = append(parts, fmt.Sprintf("%d schedule(s)", standing))
	}
	if monitors > 0 {
		parts = append(parts, fmt.Sprintf("%d monitor(s)", monitors))
	}
	if recurring > 0 {
		parts = append(parts, fmt.Sprintf("%d recurring task(s)", recurring))
	}
	if len(parts) == 0 {
		return "No standing work"
	}
	return strings.Join(parts, " · ")
}

// parkedCount counts the standing work that has stopped and is waiting on the
// owner — the number that turns the fleet pane from a report into a to-do.
func parkedCount(standing []StandingAgent, recurring []recurringTaskRow) int {
	n := 0
	for _, sa := range standing {
		if scheduleStopLabel(StandingStopCause(sa), StandingStopNote(sa)) != "" {
			n++
		}
	}
	for _, rt := range recurring {
		if rt.Payload.Broken {
			n++
		}
	}
	return n
}

// fleetSpendLabel totals the fleet's 30-day spend, priced when rates are
// configured and in tokens always — the same rendering the per-agent rows use,
// so the total and the rows below it read in one unit.
func fleetSpendLabel(rows []agentSpendRow) string {
	if len(rows) == 0 {
		return ""
	}
	var cost float64
	var tokens int64
	for _, r := range rows {
		cost += r.month
		tokens += r.tokens
	}
	if tokens == 0 && cost == 0 {
		return ""
	}
	s := HumanCount(int(tokens)) + " tokens"
	if RatesConfigured() {
		s = "$" + trimMoney(cost) + " · " + s
	}
	return s
}
