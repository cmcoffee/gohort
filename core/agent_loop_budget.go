package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// --- action quotas -----------------------------------------------------------
//
// "You may post six times a day" is a rule, and a rule the model is asked to
// keep is not a limit. Told exactly that, an agent counted its own posts out
// of a listing, mistook UTC timestamps for local ones, and posted nine — then
// reported the cap as reached. The count belongs where the calls actually
// happen.
//
// The window is a rolling 24 hours rather than a calendar day, which is the
// same choice the per-credential cap makes, and for the same reason: a
// calendar day needs a timezone, and the one thing this must never do is
// disagree with itself about when the day started.

const actionQuotaTable = "agent_action_quota"

const actionQuotaWindow = 24 * time.Hour

// actionQuotaName is the name a quota is written against: the grouped tool's
// action when the call carries one ("moltbook/create_post"), else the tool.
func actionQuotaName(tool string, args map[string]any) string {
	if a, ok := args["action"].(string); ok {
		if a = strings.TrimSpace(a); a != "" {
			return tool + "/" + a
		}
	}
	return tool
}

// SharedBudget is an allowance several runs count against: action quotas and
// a daily spend ceiling kept under one Key, in the same rolling window as an
// agent's own. Label names whose allowance it is, for the refusal ("Writer
// and the agents under it").
type SharedBudget struct {
	Key           string
	Label         string
	ActionQuotas  map[string]int
	DailySpendUSD float64
}

// runBudgets is every allowance a run is held to: its own first, then the
// shared ones. One without a key is skipped: a budget nobody owns is
// somebody else's.
func runBudgets(cfg AgentLoopConfig) []SharedBudget {
	var out []SharedBudget
	if strings.TrimSpace(cfg.BudgetKey) != "" {
		out = append(out, SharedBudget{Key: cfg.BudgetKey, ActionQuotas: cfg.ActionQuotas, DailySpendUSD: cfg.DailySpendUSD})
	}
	for _, b := range cfg.SharedBudgets {
		if strings.TrimSpace(b.Key) != "" {
			out = append(out, b)
		}
	}
	return out
}

// actionQuotaLimit finds the limit in one set of quotas, accepting either the
// exact action ("moltbook/create_post") or the bare tool ("moltbook") so a
// quota can cover a whole tool or one of its actions.
func actionQuotaLimit(quotas map[string]int, tool string, args map[string]any) (name string, limit int) {
	if len(quotas) == 0 {
		return "", 0
	}
	action := actionQuotaName(tool, args)
	if n, ok := quotas[action]; ok && n > 0 {
		return action, n
	}
	if n, ok := quotas[tool]; ok && n > 0 {
		return tool, n
	}
	return "", 0
}

// actionQuotaRefusal returns the text to hand back instead of running a call
// that is out of allowance, with the action and the limit it was judged
// against. Every budget the run is held to is checked, its own first.
func actionQuotaRefusal(cfg AgentLoopConfig, tool string, args map[string]any) (string, string, int) {
	for _, b := range runBudgets(cfg) {
		action, limit := actionQuotaLimit(b.ActionQuotas, tool, args)
		if limit <= 0 {
			continue
		}
		used, oldest := actionQuotaUsage(b.Key, action)
		if used < limit {
			continue
		}
		when := "later today"
		if !oldest.IsZero() {
			if free := time.Until(oldest.Add(actionQuotaWindow)); free > 0 {
				when = "in about " + shortDurationWords(free)
			}
		}
		whose := ""
		if b.Label != "" {
			whose = " The allowance is shared by " + b.Label + ", and their runs count toward it."
		}
		return fmt.Sprintf(
			"STOP: '%s' was NOT called. It has already run %d time(s) in the last 24 hours, which is its allowance of %d.%s This is enforced by the framework, not a rule you are asked to keep: further calls will be refused until the window frees up, %s. "+
				"Do NOT try to reach the same action another way. Finish with what you have and say plainly that the allowance is spent.",
			action, used, limit, whose, when), action, limit
	}
	return "", "", 0
}

// chargeActionQuota records one SUCCESSFUL run of a capped action, against
// every budget that caps it.
func chargeActionQuota(cfg AgentLoopConfig, tool string, args map[string]any) {
	if RootDB == nil {
		return
	}
	for _, b := range runBudgets(cfg) {
		action, limit := actionQuotaLimit(b.ActionQuotas, tool, args)
		if limit <= 0 {
			continue
		}
		var stamps []time.Time
		RootDB.Get(actionQuotaTable, b.Key+"|"+action, &stamps)
		stamps = append(pruneQuotaStamps(stamps), time.Now())
		RootDB.Set(actionQuotaTable, b.Key+"|"+action, &stamps)
	}
}

// actionQuotaUsage reports how many runs are inside the window and when the
// oldest of them was, which is when the allowance next frees up.
func actionQuotaUsage(key, action string) (used int, oldest time.Time) {
	if RootDB == nil {
		return 0, time.Time{}
	}
	var stamps []time.Time
	RootDB.Get(actionQuotaTable, key+"|"+action, &stamps)
	stamps = pruneQuotaStamps(stamps)
	if len(stamps) == 0 {
		return 0, time.Time{}
	}
	return len(stamps), stamps[0]
}

// pruneQuotaStamps drops runs that have aged out of the window, keeping the
// remainder in order.
func pruneQuotaStamps(stamps []time.Time) []time.Time {
	cutoff := time.Now().Add(-actionQuotaWindow)
	out := stamps[:0:0]
	for _, t := range stamps {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// shortDurationWords renders a wait the way a person would say it. Rounded,
// not truncated: a slot that frees in 21 hours 59 minutes is "22 hours" to
// everyone except an int conversion.
func shortDurationWords(d time.Duration) string {
	if d >= time.Hour {
		if h := int(d.Round(time.Hour).Hours()); h == 1 {
			return "an hour"
		} else {
			return fmt.Sprintf("%d hours", h)
		}
	}
	m := int(d.Round(time.Minute).Minutes())
	if m <= 1 {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// spendWhose names a shared allowance in a sentence about spend: "" for the
// run's own.
func spendWhose(label string) string {
	if label == "" {
		return ""
	}
	return ", shared by " + label
}

// --- daily spend ceiling ------------------------------------------------------
//
// Nothing stood between a scheduled agent on a frontier model and doing that
// every hour: one unattended turn was measured at over a dollar, most of it
// prompt-cache writes, and the only place it showed up was a line in a log
// nobody reads hourly. This is the ceiling, charged from what the provider
// actually reported and kept in the same rolling window as the action quotas.

const spendLedgerTable = "agent_spend_ledger"

type spendEntry struct {
	At  time.Time `json:"at"`
	USD float64   `json:"usd"`
}

// dailySpend totals what this budget has cost inside the window, dropping
// what has aged out.
func dailySpend(key string) (float64, []spendEntry) {
	if strings.TrimSpace(key) == "" || RootDB == nil {
		return 0, nil
	}
	var entries []spendEntry
	RootDB.Get(spendLedgerTable, key, &entries)
	cutoff := time.Now().Add(-actionQuotaWindow)
	kept := entries[:0:0]
	total := 0.0
	for _, e := range entries {
		if e.At.After(cutoff) {
			kept = append(kept, e)
			total += e.USD
		}
	}
	return total, kept
}

// overDailySpend reports whether any budget the run is held to is already
// spent, and which: what it has spent, its ceiling, and whose it is ("" for
// the run's own).
func overDailySpend(cfg AgentLoopConfig) (over bool, spent, limit float64, label string) {
	for _, b := range runBudgets(cfg) {
		if b.DailySpendUSD <= 0 {
			continue
		}
		if s, _ := dailySpend(b.Key); s >= b.DailySpendUSD {
			return true, s, b.DailySpendUSD, b.Label
		}
	}
	return false, 0, 0, ""
}

// chargeDailySpend bills one round to every budget with a ceiling. It
// reports the first budget this round pushed over its line (what it has
// spent, its ceiling, whose it is, crossed=true), or else the first capped
// budget's running total, the run's own when it has a ceiling.
func chargeDailySpend(cfg AgentLoopConfig, resp *Response) (spent, limit float64, label string, crossed bool) {
	if resp == nil || RootDB == nil {
		return 0, 0, "", false
	}
	var cost float64
	priced, reported := false, false
	for _, b := range runBudgets(cfg) {
		if b.DailySpendUSD <= 0 {
			continue
		}
		if !priced {
			cost, priced = roundCostUSD(resp), true
		}
		if cost <= 0 {
			return 0, 0, "", false
		}
		before, entries := dailySpend(b.Key)
		entries = append(entries, spendEntry{At: time.Now(), USD: cost})
		RootDB.Set(spendLedgerTable, b.Key, &entries)
		after := before + cost
		switch {
		case !crossed && before < b.DailySpendUSD && after >= b.DailySpendUSD:
			spent, limit, label, crossed, reported = after, b.DailySpendUSD, b.Label, true, true
		case !reported:
			spent, limit, label, reported = after, b.DailySpendUSD, b.Label, true
		}
	}
	return spent, limit, label, crossed
}

// roundCostUSD prices one response with the deployment's configured rates,
// billing it against the tier that actually served it. Zero when no rates are
// configured, which is what a local-only deployment has — and a ceiling
// measured in dollars means nothing there.
func roundCostUSD(resp *Response) float64 {
	if !RatesConfigured() {
		return 0
	}
	d := UsageDiff{}
	if resp.Tier == LEAD {
		d.LeadInput = int64(resp.InputTokens)
		d.LeadOutput = int64(resp.OutputTokens)
		d.LeadCacheRead = int64(resp.CacheReadTokens)
		d.LeadCacheWrite = int64(resp.CacheWriteTokens)
	} else {
		d.WorkerInput = int64(resp.InputTokens)
		d.WorkerOutput = int64(resp.OutputTokens)
		d.WorkerCacheRead = int64(resp.CacheReadTokens)
		d.WorkerCacheWrite = int64(resp.CacheWriteTokens)
	}
	return GetCostRates().Estimate(d)
}
