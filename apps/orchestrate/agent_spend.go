package orchestrate

// Per-agent spend — what each agent costs its owner.
//
// Every LLM call is counted: the process tracker feeds the admin's cost
// history, and each request, dispatch and scheduled fire gets its own usage
// line in the log. None of that is keyed by AGENT, so "what did my nightly
// digest cost this month" had no answer short of grepping the log for its
// run ids, and the only per-agent cost fact an owner ever saw was the
// daily-cap breadcrumb.
//
// This banks each run's own tracker — the same scoped tracker the usage line
// already reads, so nothing is counted twice and a delegated sub-agent's
// tokens land on the sub-agent, not its delegator — into a daily row per
// (owner, agent) in the root store. Spend accrues to the agent's OWNER: a
// published agent chatted by a visitor is the owner's agent running on the
// deployment's models, and the owner's console is where the number belongs.
// Priced at read time with the configured rates, so a rate change reprices
// history rather than freezing yesterday's estimate.

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	agentSpendTable   = "agent_spend_daily"
	agentSpendKeepDay = 90
)

// agentSpendDay is one (owner, agent, day) row: the token counts and how
// many runs banked into it. Name is the agent's display name at the time,
// so a deleted agent still reads as something in the pane.
type agentSpendDay struct {
	Owner   string    `json:"owner"`
	AgentID string    `json:"agent_id"`
	Name    string    `json:"name"`
	Day     string    `json:"day"` // YYYY-MM-DD, UTC
	Usage   UsageDiff `json:"usage"`
	Runs    int       `json:"runs"`
	LastAt  time.Time `json:"last_at"`
	Updated time.Time `json:"updated"`
}

func agentSpendKey(owner, agentID, day string) string { return owner + ":" + agentID + ":" + day }

// spendOwner is who a run's spend accrues to: the agent's author, or the
// runtime user for a seed (whose Owner is the framework marker) or a record
// with no owner.
func spendOwner(agent AgentRecord, runtimeUser string) string {
	if o := strings.TrimSpace(agent.Owner); o != "" && o != seedOwner {
		return o
	}
	return strings.TrimSpace(runtimeUser)
}

// bankAgentSpend adds one run's usage to the day's row. Zero usage banks
// nothing — a turn that never reached a model is not a run that cost.
func bankAgentSpend(owner string, agent AgentRecord, d UsageDiff) {
	if RootDB == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(agent.ID) == "" {
		return
	}
	if d.WorkerInput == 0 && d.WorkerOutput == 0 && d.LeadInput == 0 && d.LeadOutput == 0 &&
		d.WorkerCacheRead == 0 && d.WorkerCacheWrite == 0 && d.LeadCacheRead == 0 && d.LeadCacheWrite == 0 &&
		d.SearchCalls == 0 && d.ImageCalls == 0 {
		return
	}
	now := time.Now()
	day := now.UTC().Format("2006-01-02")
	key := agentSpendKey(owner, agent.ID, day)
	var row agentSpendDay
	fresh := !RootDB.Get(agentSpendTable, key, &row)
	if fresh {
		row = agentSpendDay{Owner: owner, AgentID: agent.ID, Day: day}
		// A new day's first bank is the cheap moment to drop what is past
		// keeping; a scan per bank would be the expensive one.
		pruneAgentSpend(owner, now)
	}
	if n := strings.TrimSpace(agent.Name); n != "" {
		row.Name = n
	}
	row.Usage = addUsage(row.Usage, d)
	row.Runs++
	row.LastAt = now
	row.Updated = now
	RootDB.Set(agentSpendTable, key, row)
}

// bankScopedSpend banks the tracker a scoped run carries on its context —
// the one WithSubUsage installed — so a dispatch or a scheduled fire bills
// to the agent it ran. Call at defer time, after the run has finished.
func bankScopedSpend(ctx context.Context, owner string, agent AgentRecord) {
	if t := RequestUsage(ctx); t != nil {
		bankAgentSpend(owner, agent, t.Snapshot())
	}
}

func addUsage(a, b UsageDiff) UsageDiff {
	return UsageDiff{
		WorkerInput: a.WorkerInput + b.WorkerInput, WorkerOutput: a.WorkerOutput + b.WorkerOutput,
		LeadInput: a.LeadInput + b.LeadInput, LeadOutput: a.LeadOutput + b.LeadOutput,
		SearchCalls: a.SearchCalls + b.SearchCalls, ImageCalls: a.ImageCalls + b.ImageCalls,
		WorkerCacheRead: a.WorkerCacheRead + b.WorkerCacheRead, WorkerCacheWrite: a.WorkerCacheWrite + b.WorkerCacheWrite,
		LeadCacheRead: a.LeadCacheRead + b.LeadCacheRead, LeadCacheWrite: a.LeadCacheWrite + b.LeadCacheWrite,
	}
}

// pruneAgentSpend drops an owner's rows older than the keep window.
func pruneAgentSpend(owner string, now time.Time) {
	if RootDB == nil {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -agentSpendKeepDay).Format("2006-01-02")
	prefix := owner + ":"
	for _, k := range RootDB.Keys(agentSpendTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if day := k[strings.LastIndex(k, ":")+1:]; day < cutoff {
			RootDB.Unset(agentSpendTable, k)
		}
	}
}

// agentSpendRow is one card in the Spend pane: the agent, then the windows,
// priced when rates are configured and in tokens always.
type agentSpendRow struct {
	Agent  string `json:"agent"`
	Today  string `json:"today"`
	Week   string `json:"7_days"`
	Month  string `json:"30_days"`
	Runs   string `json:"runs_30d"`
	Last   string `json:"last_active"`
	ID     string `json:"_id"`
	month  float64
	tokens int64
}

// agentSpendRows aggregates an owner's rows into per-agent windows, most
// expensive over 30 days first. Windows are UTC days, matching the keys.
func agentSpendRows(owner string, now time.Time, loc *time.Location) []agentSpendRow {
	if RootDB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	type acc struct {
		name               string
		today, week, month UsageDiff
		runs               int
		last               time.Time
	}
	today := now.UTC().Format("2006-01-02")
	weekFrom := now.UTC().AddDate(0, 0, -6).Format("2006-01-02")
	monthFrom := now.UTC().AddDate(0, 0, -29).Format("2006-01-02")
	byAgent := map[string]*acc{}
	prefix := owner + ":"
	for _, k := range RootDB.Keys(agentSpendTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var row agentSpendDay
		if !RootDB.Get(agentSpendTable, k, &row) || row.Day < monthFrom {
			continue
		}
		a := byAgent[row.AgentID]
		if a == nil {
			a = &acc{}
			byAgent[row.AgentID] = a
		}
		if row.Name != "" {
			a.name = row.Name
		}
		a.month = addUsage(a.month, row.Usage)
		a.runs += row.Runs
		if row.Day >= weekFrom {
			a.week = addUsage(a.week, row.Usage)
		}
		if row.Day == today {
			a.today = addUsage(a.today, row.Usage)
		}
		if row.LastAt.After(a.last) {
			a.last = row.LastAt
		}
	}
	rates := GetCostRates()
	priced := RatesConfigured()
	render := func(d UsageDiff) string {
		tokens := d.WorkerInput + d.WorkerOutput + d.LeadInput + d.LeadOutput + d.WorkerCacheRead + d.WorkerCacheWrite + d.LeadCacheRead + d.LeadCacheWrite
		if tokens == 0 && d.SearchCalls == 0 && d.ImageCalls == 0 {
			return "·"
		}
		s := HumanCount(int(tokens)) + " tokens"
		if priced {
			s = "$" + trimMoney(rates.Estimate(d)) + " · " + s
		}
		return s
	}
	rows := make([]agentSpendRow, 0, len(byAgent))
	for id, a := range byAgent {
		name := a.name
		if name == "" {
			name = id
		}
		r := agentSpendRow{
			Agent: name, Today: render(a.today), Week: render(a.week), Month: render(a.month),
			Runs: strconv.Itoa(a.runs) + " run(s)", ID: id,
			month:  rates.Estimate(a.month),
			tokens: a.month.WorkerInput + a.month.WorkerOutput + a.month.LeadInput + a.month.LeadOutput,
		}
		if !a.last.IsZero() {
			r.Last = a.last.In(loc).Format("Jan 2 15:04")
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].month != rows[j].month {
			return rows[i].month > rows[j].month
		}
		if rows[i].tokens != rows[j].tokens {
			return rows[i].tokens > rows[j].tokens
		}
		return rows[i].Agent < rows[j].Agent
	})
	return rows
}

// trimMoney renders an estimate with the precision small numbers need:
// cents for anything under a dollar, whole cents above.
func trimMoney(v float64) string {
	if v < 0.01 && v > 0 {
		return "<0.01"
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
}

// handleConsoleSpend serves the pane.
func (T *OrchestrateApp) handleConsoleSpend(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := agentSpendRows(user, time.Now(), UserLocation(user))
	if rows == nil {
		rows = []agentSpendRow{}
	}
	writeJSON(w, rows)
}
