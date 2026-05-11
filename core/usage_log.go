// Daily-rollup persistence for ProcessUsage events.
//
// Every successful image-gen / search / worker LLM / lead LLM call
// updates an entry in a per-day rollup table keyed by YYYY-MM-DD.
// The admin cost-history chart's CostRecordScanner reads from this
// rollup so the chart reflects every cost-incurring call regardless
// of which app made it. Apps that already persist Usage on their own
// records (research, debate) feed the chart through their own
// scanners; this rollup catches the rest.
//
// Storage shape: one record per UTC day with the same UsageDiff
// fields the chart already consumes. Read-modify-write is serialized
// via a process-wide mutex so two concurrent calls on the same day
// can't lose updates to a race.

package core

import (
	"sync"
	"time"
)

const usageDailyTable = "usage_daily"

// usageBackfillFlagKey marks the backfill as completed in RootDB so
// it runs exactly once across restarts, even if records are added or
// removed afterwards.
const usageBackfillFlagKey = "_backfill_done"

var usageDailyMu sync.Mutex

// usageBackfillFunc is the per-app shape registered via
// RegisterUsageBackfill. Returns every persisted record's Usage as
// (Date, UsageDiff) pairs. Date is whatever the app stores — the
// merger only uses the YYYY-MM-DD prefix.
type usageBackfillFunc func() []DatedUsage

type usageBackfillSource struct {
	label string
	fn    usageBackfillFunc
}

var (
	usageBackfillMu      sync.Mutex
	usageBackfillSources []usageBackfillSource
)

// RegisterUsageBackfill registers a per-app source of pre-rollup
// historical Usage data. Called from each app's init(). On the first
// call to RunUsageBackfill (driven by main at startup), every source
// is walked and any day not already in usage_daily is seeded from
// the per-app records.
func RegisterUsageBackfill(label string, fn usageBackfillFunc) {
	usageBackfillMu.Lock()
	usageBackfillSources = append(usageBackfillSources, usageBackfillSource{label: label, fn: fn})
	usageBackfillMu.Unlock()
}

// RunUsageBackfill seeds usage_daily from every registered backfill
// source, but only on first invocation per database lifetime — a
// flag in RootDB tracks completion. Subsequent calls are cheap no-ops.
// Days that already have a rollup entry are NEVER overwritten, so
// rerunning by hand (after clearing the flag) is safe.
//
// Call from main after every app's init() has registered its source
// (i.e. after RootDB is set and the app registry is built).
func RunUsageBackfill() {
	if RootDB == nil {
		return
	}
	usageDailyMu.Lock()
	var done bool
	if RootDB.Get(usageDailyTable, usageBackfillFlagKey, &done) && done {
		usageDailyMu.Unlock()
		return
	}
	usageDailyMu.Unlock()

	usageBackfillMu.Lock()
	sources := append([]usageBackfillSource{}, usageBackfillSources...)
	usageBackfillMu.Unlock()

	if len(sources) == 0 {
		// Still mark done so we don't keep checking each startup.
		usageDailyMu.Lock()
		RootDB.Set(usageDailyTable, usageBackfillFlagKey, true)
		usageDailyMu.Unlock()
		return
	}

	// Aggregate per-app records into a per-day map.
	merged := map[string]DailyCost{}
	for _, src := range sources {
		for _, e := range src.fn() {
			if len(e.Date) < 10 {
				continue
			}
			day := e.Date[:10]
			rec := merged[day]
			rec.Date = day
			rec.WorkerInput += e.Usage.WorkerInput
			rec.WorkerOutput += e.Usage.WorkerOutput
			rec.LeadInput += e.Usage.LeadInput
			rec.LeadOutput += e.Usage.LeadOutput
			rec.SearchCalls += e.Usage.SearchCalls
			rec.ImageCalls += e.Usage.ImageCalls
			rec.RunCount++
			merged[day] = rec
		}
	}

	usageDailyMu.Lock()
	defer usageDailyMu.Unlock()
	wrote := 0
	for day, rec := range merged {
		var existing DailyCost
		if RootDB.Get(usageDailyTable, day, &existing) {
			continue // rollup is authoritative
		}
		rec.Cost = GetCostRates().Estimate(UsageDiff{
			WorkerInput:  rec.WorkerInput,
			WorkerOutput: rec.WorkerOutput,
			LeadInput:    rec.LeadInput,
			LeadOutput:   rec.LeadOutput,
			SearchCalls:  rec.SearchCalls,
			ImageCalls:   rec.ImageCalls,
		})
		RootDB.Set(usageDailyTable, day, rec)
		wrote++
	}
	RootDB.Set(usageDailyTable, usageBackfillFlagKey, true)
	if wrote > 0 {
		Log("[usage_log] backfill seeded %d historical day(s) from %d app source(s)", wrote, len(sources))
	}
}

// recordDailyUsage rolls a single call's UsageDiff into today's
// entry. No-op when RootDB is unset (early init, CLI tools without a
// dashboard). Errors are silently dropped because cost logging must
// never fail a working call.
func recordDailyUsage(diff UsageDiff) {
	if RootDB == nil {
		return
	}
	if diff.WorkerInput == 0 && diff.WorkerOutput == 0 &&
		diff.LeadInput == 0 && diff.LeadOutput == 0 &&
		diff.SearchCalls == 0 && diff.ImageCalls == 0 {
		return
	}
	day := time.Now().UTC().Format("2006-01-02")
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
	rec.RunCount++
	// Recompute cost using the current rates so the stored cost
	// reflects whatever rates were configured at call time. The
	// admin chart re-derives cost on render anyway, but having it
	// stored makes ad-hoc DB inspection meaningful.
	rec.Cost = GetCostRates().Estimate(UsageDiff{
		WorkerInput:  rec.WorkerInput,
		WorkerOutput: rec.WorkerOutput,
		LeadInput:    rec.LeadInput,
		LeadOutput:   rec.LeadOutput,
		SearchCalls:  rec.SearchCalls,
		ImageCalls:   rec.ImageCalls,
	})
	RootDB.Set(usageDailyTable, day, rec)
}

// scanDailyUsage returns every persisted day rollup as a DatedUsage
// stream. Hooked into the cost-history aggregator via init() below.
func scanDailyUsage() []DatedUsage {
	if RootDB == nil {
		return nil
	}
	usageDailyMu.Lock()
	defer usageDailyMu.Unlock()
	var out []DatedUsage
	for _, k := range RootDB.Keys(usageDailyTable) {
		if k == usageBackfillFlagKey {
			continue
		}
		var rec DailyCost
		if !RootDB.Get(usageDailyTable, k, &rec) {
			continue
		}
		out = append(out, DatedUsage{
			Date: rec.Date,
			Usage: UsageDiff{
				WorkerInput:  rec.WorkerInput,
				WorkerOutput: rec.WorkerOutput,
				LeadInput:    rec.LeadInput,
				LeadOutput:   rec.LeadOutput,
				SearchCalls:  rec.SearchCalls,
				ImageCalls:   rec.ImageCalls,
			},
		})
	}
	return out
}

func init() {
	RegisterCostRecordScanner("Daily call rollup", scanDailyUsage)
}
