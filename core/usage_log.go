// Daily-rollup persistence for ProcessUsage events.
//
// Every successful image-gen / search / worker LLM / lead LLM call
// updates an entry in a per-day rollup table keyed by YYYY-MM-DD.
// The admin cost-history chart's CostRecordScanner reads from this
// rollup so the chart reflects every cost-incurring call regardless
// of which app made it.
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

var usageDailyMu sync.Mutex

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
