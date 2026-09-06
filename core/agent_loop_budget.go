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

// actionQuotaLimit finds the limit that applies, accepting either the exact
// action ("moltbook/create_post") or the bare tool ("moltbook") so a quota can
// cover a whole tool or one of its actions.
func actionQuotaLimit(cfg AgentLoopConfig, tool string, args map[string]any) (name string, limit int) {
	if len(cfg.ActionQuotas) == 0 || strings.TrimSpace(cfg.BudgetKey) == "" {
		return "", 0
	}
	action := actionQuotaName(tool, args)
	if n, ok := cfg.ActionQuotas[action]; ok && n > 0 {
		return action, n
	}
	if n, ok := cfg.ActionQuotas[tool]; ok && n > 0 {
		return tool, n
	}
	return "", 0
}

// actionQuotaRefusal returns the text to hand back instead of running a call
// that is out of allowance, and the action it was judged against.
func actionQuotaRefusal(cfg AgentLoopConfig, tool string, args map[string]any) (string, string) {
	action, limit := actionQuotaLimit(cfg, tool, args)
	if limit <= 0 {
		return "", ""
	}
	used, oldest := actionQuotaUsage(cfg.BudgetKey, action)
	if used < limit {
		return "", action
	}
	when := "later today"
	if !oldest.IsZero() {
		if free := time.Until(oldest.Add(actionQuotaWindow)); free > 0 {
			when = "in about " + shortDurationWords(free)
		}
	}
	return fmt.Sprintf(
		"STOP — '%s' was NOT called. It has already run %d time(s) in the last 24 hours, which is its allowance of %d. This is enforced by the framework, not a rule you are asked to keep: further calls will be refused until the window frees up, %s. "+
			"Do NOT try to reach the same action another way. Finish with what you have and say plainly that the allowance is spent.",
		action, used, limit, when), action
}

// chargeActionQuota records one SUCCESSFUL run of a capped action.
func chargeActionQuota(cfg AgentLoopConfig, tool string, args map[string]any) {
	action, limit := actionQuotaLimit(cfg, tool, args)
	if limit <= 0 || RootDB == nil {
		return
	}
	key := cfg.BudgetKey
	var stamps []time.Time
	RootDB.Get(actionQuotaTable, key+"|"+action, &stamps)
	stamps = append(pruneQuotaStamps(stamps), time.Now())
	RootDB.Set(actionQuotaTable, key+"|"+action, &stamps)
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

// overDailySpend reports whether this budget is already spent.
func overDailySpend(cfg AgentLoopConfig) (bool, float64) {
	if cfg.DailySpendUSD <= 0 || strings.TrimSpace(cfg.BudgetKey) == "" {
		return false, 0
	}
	spent, _ := dailySpend(cfg.BudgetKey)
	return spent >= cfg.DailySpendUSD, spent
}

// chargeDailySpend bills one round and reports the running total, plus
// whether this round is the one that crossed the line.
func chargeDailySpend(cfg AgentLoopConfig, resp *Response) (float64, bool) {
	if cfg.DailySpendUSD <= 0 || strings.TrimSpace(cfg.BudgetKey) == "" || resp == nil || RootDB == nil {
		return 0, false
	}
	cost := roundCostUSD(resp)
	if cost <= 0 {
		return 0, false
	}
	before, entries := dailySpend(cfg.BudgetKey)
	entries = append(entries, spendEntry{At: time.Now(), USD: cost})
	RootDB.Set(spendLedgerTable, cfg.BudgetKey, &entries)
	after := before + cost
	return after, before < cfg.DailySpendUSD && after >= cfg.DailySpendUSD
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
