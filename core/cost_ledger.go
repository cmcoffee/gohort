// Per-source external-call cost ledger — the "cost hooks" substrate. Source
// hooks and SecureAPI credentials hit metered third-party APIs whose spend was
// invisible to the cost chart (which only knew LLM tokens + search/image
// calls). When such a call fires and its record carries a per-call cost, it's
// recorded here as a dated per-source rollup; the admin cost surfaces read it
// for the chart total (folded in at aggregation) and a per-source breakdown
// table.
//
// This is the first "cost source registry" instance: the cost set is now
// DYNAMIC (one entry per hook/credential) rather than the four fixed buckets in
// CostRates. Stored in a settable global DB so the deep call sites
// (QuerySourceHook, dispatchToolCall) can record without threading a handle.

package core

import (
	"sort"
	"sync"
	"time"
)

// costLedgerTable rows are keyed "<YYYY-MM-DD>|<source_id>" so a per-day
// per-source rollup is a single read-modify-write and date-window scans are a
// prefix-free filter on the parsed Date field.
const costLedgerTable = "core_cost_ledger"

// CostLedgerRow is one (day, source) rollup: how many metered calls that source
// made that day and their summed dollar cost.
type CostLedgerRow struct {
	Date     string  `json:"date"`
	SourceID string  `json:"source_id"` // "hook:<name>" / "cred:<name>"
	Label    string  `json:"label"`     // display name
	Calls    int64   `json:"calls"`
	Cost     float64 `json:"cost"`
}

// CostSourceSummary is one source's total over a window — for the admin
// "Cost by source" table.
type CostSourceSummary struct {
	SourceID string  `json:"source_id"`
	Label    string  `json:"label"`
	Calls    int64   `json:"calls"`
	Cost     float64 `json:"cost"`
}

var (
	costLedgerMu sync.Mutex
	costLedgerDB Database
)

// SetCostLedgerDB wires the DB that holds the external-cost ledger. Call once
// at startup with the main DB. A nil DB makes RecordExternalCost a no-op.
func SetCostLedgerDB(db Database) {
	costLedgerMu.Lock()
	costLedgerDB = db
	costLedgerMu.Unlock()
}

// RecordExternalCost adds one metered external call to today's per-source
// rollup. No-op when costPerCall <= 0, sourceID is empty, or the ledger DB
// isn't wired (so it's free to call unconditionally at a fire site).
func RecordExternalCost(sourceID, label string, costPerCall float64) {
	if costPerCall <= 0 || sourceID == "" {
		return
	}
	costLedgerMu.Lock()
	defer costLedgerMu.Unlock()
	db := costLedgerDB
	if db == nil {
		return
	}
	date := time.Now().Format("2006-01-02")
	key := date + "|" + sourceID
	var row CostLedgerRow
	db.Get(costLedgerTable, key, &row)
	row.Date = date
	row.SourceID = sourceID
	row.Label = label
	row.Calls++
	row.Cost += costPerCall
	db.Set(costLedgerTable, key, row)
}

// costLedgerRows returns every ledger row dated within the last `days`
// (inclusive of today). days <= 0 returns all rows.
func costLedgerRows(days int) []CostLedgerRow {
	costLedgerMu.Lock()
	db := costLedgerDB
	costLedgerMu.Unlock()
	if db == nil {
		return nil
	}
	cutoff := ""
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	}
	var out []CostLedgerRow
	for _, k := range db.Keys(costLedgerTable) {
		var row CostLedgerRow
		if !db.Get(costLedgerTable, k, &row) {
			continue
		}
		if cutoff != "" && row.Date < cutoff {
			continue
		}
		out = append(out, row)
	}
	return out
}

// CostExternalDaily returns date → total external cost over the last `days`,
// for folding into the per-day cost chart total.
func CostExternalDaily(days int) map[string]float64 {
	out := map[string]float64{}
	for _, row := range costLedgerRows(days) {
		out[row.Date] += row.Cost
	}
	return out
}

// CostBySource returns each source's total over the last `days`, sorted by
// cost descending — the admin "Cost by source" breakdown table.
func CostBySource(days int) []CostSourceSummary {
	agg := map[string]*CostSourceSummary{}
	for _, row := range costLedgerRows(days) {
		s := agg[row.SourceID]
		if s == nil {
			s = &CostSourceSummary{SourceID: row.SourceID, Label: row.Label}
			agg[row.SourceID] = s
		}
		s.Calls += row.Calls
		s.Cost += row.Cost
		if row.Label != "" {
			s.Label = row.Label
		}
	}
	out := make([]CostSourceSummary, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Label < out[j].Label
	})
	return out
}
