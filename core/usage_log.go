// Daily-rollup persistence for ProcessUsage events.
//
// Every successful image-gen / search / worker LLM / lead LLM call
// updates an entry in a per-day rollup table keyed by YYYY-MM-DD.
// The admin cost-history chart's CostRecordScanner reads from this
// rollup so the chart reflects every cost-incurring call regardless
// of which app made it.
//
// Storage shape: one record per local-calendar day with the same UsageDiff
// fields the chart already consumes. Read-modify-write is serialized
// via a process-wide mutex so two concurrent calls on the same day
// can't lose updates to a race.

package core

import (
	"sync"
	"time"
)

const usageDailyTable = "usage_daily"

// dropLegacyUsageDaily wipes the usage_daily table once per deployment.
// The original table held pre-DailyCost values (a non-struct shape)
// that the kvlite layer Fatal-exits on when decoded into the current
// DailyCost type. Drop-and-start-fresh is right because the chart
// re-aggregates from the registered scanners on every render and
// nothing else reads this table — old data was never accessible.
//
// Fires via MigrationRunner so the marker shows up in the admin
// Migrations section ("ran at X, dropped N keys") and never repeats.
func dropLegacyUsageDaily() {
	NewMigrationRunner("core", "").Once("drop_legacy_usage_daily:v1", func() int {
		if RootDB == nil {
			return 0
		}
		n := len(RootDB.Keys(usageDailyTable))
		RootDB.Drop(usageDailyTable)
		return n
	})
}

var (
	usageDailyMu       sync.Mutex
	usageDailyDropOnce sync.Once
	usageDropWarnOnce  sync.Once
)

// recordDailyUsage rolls a single call's UsageDiff into today's
// entry. No-op when RootDB is unset (early init, CLI tools without a
// dashboard). Errors are silently dropped because cost logging must
// never fail a working call.
func recordDailyUsage(diff UsageDiff) {
	if RootDB == nil {
		// Said once, because the alternative is what happened: a
		// deployment reporting "Est. cost: $3.0001" per request from the
		// in-process tracker while the admin cost history stayed empty,
		// with nothing anywhere connecting the two. Cost logging still
		// must not fail the call — this only makes the drop visible.
		usageDropWarnOnce.Do(func() {
			Log("[usage] no root database is wired, so per-day cost history is NOT being recorded. " +
				"Per-request totals still add up in memory; the admin cost chart will stay empty. " +
				"This means init_database() ran on a path that did not set RootDB.")
		})
		return
	}
	if diff == (UsageDiff{}) {
		// Compared whole rather than field-by-field: the old list predated the
		// cache counters, so a call that was ENTIRELY a cache hit — the most
		// expensive kind — matched "nothing happened" and was dropped before it
		// could be recorded.
		return
	}
	usageDailyDropOnce.Do(dropLegacyUsageDaily)
	// Key by LOCAL calendar day so the rollup boundary matches every
	// other day-scoped surface (recurring caps, the external cost
	// ledger, the date the user sees on their turn). See the invariant
	// note in AggregateDailyCost: the chart window MUST be derived from
	// the same zone these keys use.
	day := time.Now().Format("2006-01-02")
	usageDailyMu.Lock()
	defer usageDailyMu.Unlock()
	var rec DailyCost
	RootDB.Get(usageDailyTable, day, &rec)
	rec.Date = day
	rec.WorkerInput += diff.WorkerInput
	rec.WorkerOutput += diff.WorkerOutput
	rec.LeadInput += diff.LeadInput
	rec.LeadOutput += diff.LeadOutput
	rec.SearchCalls += diff.SearchCalls
	rec.ImageCalls += diff.ImageCalls
	rec.WorkerCacheRead += diff.WorkerCacheRead
	rec.WorkerCacheWrite += diff.WorkerCacheWrite
	rec.LeadCacheRead += diff.LeadCacheRead
	rec.LeadCacheWrite += diff.LeadCacheWrite
	rec.RunCount++
	// Recompute cost and the whole-prompt totals using the current rates so
	// the stored row reflects whatever rates were configured at call time. The
	// admin chart re-derives both on render anyway, but having them stored
	// makes ad-hoc DB inspection meaningful.
	rec.Price(GetCostRates())
	RootDB.Set(usageDailyTable, day, rec)
}

// dailyCostUsage projects a stored day rollup back into a UsageDiff. One
// place, because the two that existed had to be kept in step by hand and the
// cache counters were exactly the kind of field one of them would miss.
func dailyCostUsage(rec DailyCost) UsageDiff {
	return UsageDiff{
		WorkerInput:      rec.WorkerInput,
		WorkerOutput:     rec.WorkerOutput,
		LeadInput:        rec.LeadInput,
		LeadOutput:       rec.LeadOutput,
		SearchCalls:      rec.SearchCalls,
		ImageCalls:       rec.ImageCalls,
		WorkerCacheRead:  rec.WorkerCacheRead,
		WorkerCacheWrite: rec.WorkerCacheWrite,
		LeadCacheRead:    rec.LeadCacheRead,
		LeadCacheWrite:   rec.LeadCacheWrite,
	}
}

// scanDailyUsage returns every persisted day rollup as a DatedUsage
// stream. Hooked into the cost-history aggregator via init() below.
func scanDailyUsage() []DatedUsage {
	if RootDB == nil {
		return nil
	}
	usageDailyDropOnce.Do(dropLegacyUsageDaily)
	usageDailyMu.Lock()
	defer usageDailyMu.Unlock()
	var out []DatedUsage
	for _, k := range RootDB.Keys(usageDailyTable) {
		var rec DailyCost
		if !RootDB.Get(usageDailyTable, k, &rec) {
			continue
		}
		out = append(out, DatedUsage{Date: rec.Date, Usage: dailyCostUsage(rec)})
	}
	return out
}

func init() {
	RegisterCostRecordScanner("Daily call rollup", scanDailyUsage)
}
