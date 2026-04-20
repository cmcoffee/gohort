package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// CostRates holds the dollar prices used to convert UsageDiff into
// an estimated run cost. All token rates are dollars per 1,000
// tokens (the convention every commercial LLM provider prices in);
// search is dollars per call. Zero values are legal and produce a
// zero cost estimate — which is the correct signal when rates
// haven't been configured for this deployment.
//
// Rates are kept separate from usage tracking on purpose: the
// tracker only knows "this many tokens went through worker vs. lead."
// The pricing model that turns that into money depends on which
// specific models the deployment is using, which is a configuration
// concern, not a pipeline concern.
type CostRates struct {
	WorkerInputPer1K  float64 `json:"worker_input_per_1k"`
	WorkerOutputPer1K float64 `json:"worker_output_per_1k"`
	LeadInputPer1K    float64 `json:"lead_input_per_1k"`
	LeadOutputPer1K   float64 `json:"lead_output_per_1k"`
	SearchPerCall     float64 `json:"search_per_call"`
	ImagePerCall      float64 `json:"image_per_call"`
}

// Estimate computes the estimated dollar cost of a UsageDiff under
// these rates. Pure function; no side effects; returns 0 when
// both rates and usage are empty (the only ambiguous case for a
// reader, resolved downstream by labeling the cost as "est.").
func (r CostRates) Estimate(d UsageDiff) float64 {
	cost := 0.0
	cost += float64(d.WorkerInput) / 1000.0 * r.WorkerInputPer1K
	cost += float64(d.WorkerOutput) / 1000.0 * r.WorkerOutputPer1K
	cost += float64(d.LeadInput) / 1000.0 * r.LeadInputPer1K
	cost += float64(d.LeadOutput) / 1000.0 * r.LeadOutputPer1K
	cost += float64(d.SearchCalls) * r.SearchPerCall
	cost += float64(d.ImageCalls) * r.ImagePerCall
	return cost
}

// Process-wide rates. The application sets these at startup (from
// DB config or admin UI); pipeline code reads them at estimation
// time so rate changes propagate without restart. The `configured`
// flag tracks whether rates have ever been explicitly set — separate
// from "all values are zero," because $0.00 is a legitimate rate
// (e.g., a local-Ollama worker costs nothing but the operator has
// still been through setup).
var (
	costRatesMu         sync.RWMutex
	costRates           CostRates
	costRatesConfigured bool
)

// SetCostRates installs the process-wide cost rates and marks rates
// as configured. Safe to call at any time; subsequent Estimate reads
// will see the new values.
func SetCostRates(r CostRates) {
	costRatesMu.Lock()
	costRates = r
	costRatesConfigured = true
	costRatesMu.Unlock()
}

// GetCostRates returns a snapshot of the current cost rates. Callers
// get a value, not a pointer — no lock needed after return.
func GetCostRates() CostRates {
	costRatesMu.RLock()
	defer costRatesMu.RUnlock()
	return costRates
}

// RatesConfigured reports whether cost rates have been explicitly
// set (via --setup save, admin-UI save, or load from an existing DB
// record). Distinct from "rates are all zero" — a legitimate free-
// tier deployment still registers as configured once saved. Used to
// decide between the "$0.00" estimate and the "rates not configured"
// label on the per-run telemetry line.
func RatesConfigured() bool {
	costRatesMu.RLock()
	defer costRatesMu.RUnlock()
	return costRatesConfigured
}

// costRatesTable is the kvlite bucket + key where persisted rates live.
// Scoped as constants so both reader and writer use the same storage
// coordinates and future migrations can change the bucket name in
// one place.
const (
	costRatesTable = "cost_rates"
	costRatesKey   = "current"
)

// LoadCostRatesFromDB reads persisted rates from kvlite and installs
// them via SetCostRates. Silent no-op when the record doesn't exist
// or the DB is nil. Returns true if rates were loaded from storage.
func LoadCostRatesFromDB(db Database) (loaded bool) {
	if db == nil {
		return false
	}
	var stored CostRates
	if !db.Get(costRatesTable, costRatesKey, &stored) {
		return false
	}
	SetCostRates(stored)
	return true
}

// SaveCostRatesToDB persists the given rates to kvlite. Called by
// the admin UI handler when the operator updates rate values via
// the web form.
func SaveCostRatesToDB(db Database, r CostRates) error {
	if db == nil {
		return fmt.Errorf("no database available")
	}
	db.Set(costRatesTable, costRatesKey, r)
	return nil
}

// InitCostRates is the startup wiring. Rates live in the kvlite DB,
// set via --setup or the admin WebUI; zero values mean "rates not
// configured" and cost estimates are labeled accordingly so the
// operator can tell config has been skipped vs. a genuinely free run.
func InitCostRates(db Database) {
	if LoadCostRatesFromDB(db) {
		Debug("[cost] rates loaded from database")
		return
	}
	Debug("[cost] no rates configured — cost estimates will show 'rates not configured'")
}

// AsJSON returns the rates as a JSON string, used by the admin UI
// to prefill the edit form. Kept here so the admin page doesn't
// need to know the struct shape.
func (r CostRates) AsJSON() string {
	b, err := json.Marshal(r)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// FormatUsage returns a single-line human-readable summary of a
// UsageDiff under the current cost rates. Used for end-of-run log
// lines so operators can see at a glance what a run consumed.
//
// Example output:
//
//	tokens: worker in=125430 out=8120, lead in=42180 out=3450; searches: 18; images: 1; est. $0.47
//
// When rates are unset, the cost clause becomes "rates not configured"
// so an operator can see they've forgotten a setup step rather than
// misreading "$0.00" as actual zero cost.
func FormatUsage(d UsageDiff) string {
	rates := GetCostRates()
	tokens := fmt.Sprintf("tokens: worker in=%d out=%d, lead in=%d out=%d",
		d.WorkerInput, d.WorkerOutput, d.LeadInput, d.LeadOutput)
	searches := fmt.Sprintf("searches: %d", d.SearchCalls)
	images := fmt.Sprintf("images: %d", d.ImageCalls)
	var cost string
	if !RatesConfigured() {
		cost = "cost: rates not configured"
	} else {
		cost = fmt.Sprintf("est. $%.4f", rates.Estimate(d))
	}
	return fmt.Sprintf("%s; %s; %s; %s", tokens, searches, images, cost)
}

// FormatUsageReport renders a UsageDiff as a multi-line, kitebroker-tally-
// style block suitable for operator-facing end-of-run summaries. More
// scannable than the single-line FormatUsage when a run spends
// meaningfully across multiple columns.
//
// Example output:
//
//	[my-op-e56fd6d0]
//	  Worker tokens: in=1,666,688 out=169,033
//	  Lead tokens:   in=161,010 out=14,921
//	  Searches:      157
//	  Images:        0
//	  Est. cost:     $0.2389
func FormatUsageReport(label string, d UsageDiff) string {
	rates := GetCostRates()
	var cost string
	if !RatesConfigured() {
		cost = "rates not configured"
	} else {
		cost = fmt.Sprintf("$%.4f", rates.Estimate(d))
	}
	return fmt.Sprintf(
		"[%s]\n  Worker tokens: in=%d out=%d\n  Lead tokens:   in=%d out=%d\n  Searches:      %d\n  Images:        %d\n  Est. cost:     %s",
		label,
		d.WorkerInput, d.WorkerOutput,
		d.LeadInput, d.LeadOutput,
		d.SearchCalls, d.ImageCalls, cost)
}

// BuildDiff aggregates per-session token counts with search/image
// deltas against a pre-captured global snapshot. The result is a
// UsageDiff suitable for persistence on a run record or for passing to
// FormatUsageReport / rates.Estimate. Separated from Report so save
// sites that need to embed the diff in a record can reuse the same
// aggregation the defer-log uses.
//
// globalStart should be a snapshot taken at the start of the logical
// operation; the search/image fields come from the delta since then.
// Token fields come from the sessions — accurate per-op even under
// concurrent runs that share the process tracker.
func BuildDiff(globalStart UsageSnapshot, sessions ...*Session) UsageDiff {
	var d UsageDiff
	for _, s := range sessions {
		if s == nil {
			continue
		}
		x := s.AsDiff()
		d.WorkerInput += x.WorkerInput
		d.WorkerOutput += x.WorkerOutput
		d.LeadInput += x.LeadInput
		d.LeadOutput += x.LeadOutput
	}
	g := ProcessUsage().Diff(globalStart)
	d.SearchCalls = g.SearchCalls
	d.ImageCalls = g.ImageCalls
	return d
}

// DatedUsage pairs a record's date with its per-run UsageDiff. Produced
// by cost-record scanners (one per spend-bearing record type) and
// consumed by AggregateDailyCost to build per-day totals for the
// admin cost-history chart.
type DatedUsage struct {
	Date  string    `json:"date"`
	Usage UsageDiff `json:"usage"`
}

// DailyCost is one row of the per-day cost chart: tokens + calls
// summed across every record for that day, plus the dollar total
// priced under the currently configured CostRates.
type DailyCost struct {
	Date         string  `json:"date"`
	Cost         float64 `json:"cost"`
	WorkerInput  int64   `json:"worker_input"`
	WorkerOutput int64   `json:"worker_output"`
	LeadInput    int64   `json:"lead_input"`
	LeadOutput   int64   `json:"lead_output"`
	SearchCalls  int64   `json:"search_calls"`
	ImageCalls   int64   `json:"image_calls"`
	RunCount     int     `json:"run_count"`
}

type costScanner struct {
	label string
	fn    func() []DatedUsage
}

var (
	costScannersMu sync.Mutex
	costScanners   []costScanner
)

// RegisterCostRecordScanner registers a function that returns the
// DatedUsage entries for a record table. Called from a package's
// init() so the admin cost-history endpoint can aggregate across all
// spend-bearing record types without importing each package directly.
// Cross-package decoupling pattern.
//
// `label` is the human-readable name of the registering app (e.g.,
// the agent name). Shown in the admin panel so operators can see at a
// glance which apps are contributing to the cost chart.
//
// Scanners take no args and resolve their own DB internally (each
// agent's records live in its own bucket — global.db.Bucket(name) —
// so admin's root DB can't locate them directly). Scanners should
// return one DatedUsage per persisted record whose Usage is populated.
// Records missing Usage (legacy, pre-instrument) are skipped. For
// record types whose Usage is already rolled up into a parent record
// (e.g., sub-operations that share sessions with a parent), the
// scanner should SKIP them to avoid double-counting.
func RegisterCostRecordScanner(label string, fn func() []DatedUsage) {
	costScannersMu.Lock()
	costScanners = append(costScanners, costScanner{label: label, fn: fn})
	costScannersMu.Unlock()
}

// CollectAllUsage walks every registered cost-record scanner and
// concatenates their results. Called by the admin cost-history
// endpoint before aggregation.
func CollectAllUsage() []DatedUsage {
	costScannersMu.Lock()
	scanners := append([]costScanner{}, costScanners...)
	costScannersMu.Unlock()
	var out []DatedUsage
	for _, s := range scanners {
		out = append(out, s.fn()...)
	}
	return out
}

// RegisteredCostSources returns the labels of every package that
// registered a cost-record scanner at init time. Used by the admin
// UI to display which apps are contributing to the cost chart —
// keeping the public admin code decoupled from private-app names
// (labels come from the registering package, not a hardcoded list).
func RegisteredCostSources() []string {
	costScannersMu.Lock()
	defer costScannersMu.Unlock()
	out := make([]string, 0, len(costScanners))
	for _, s := range costScanners {
		out = append(out, s.label)
	}
	sort.Strings(out)
	return out
}

// AggregateDailyCost groups DatedUsage entries by day (YYYY-MM-DD),
// sums token/call fields, prices the total at current CostRates, and
// returns a slice sorted by date ascending. Dates are truncated to the
// first 10 characters (RFC3339 date portion); entries without a date
// are skipped.
//
// When days > 0, returns exactly that many consecutive trailing days
// ending at today's date (local timezone), including days with zero
// activity — the chart then shows a continuous timeline instead of
// skipping empty days. When days <= 0, returns only days that had at
// least one record (no zero-padding).
func AggregateDailyCost(records []DatedUsage, days int) []DailyCost {
	rates := GetCostRates()
	daily := map[string]*DailyCost{}
	for _, r := range records {
		if r.Date == "" {
			continue
		}
		day := r.Date
		if len(day) > 10 {
			day = day[:10]
		}
		d, ok := daily[day]
		if !ok {
			d = &DailyCost{Date: day}
			daily[day] = d
		}
		d.WorkerInput += r.Usage.WorkerInput
		d.WorkerOutput += r.Usage.WorkerOutput
		d.LeadInput += r.Usage.LeadInput
		d.LeadOutput += r.Usage.LeadOutput
		d.SearchCalls += r.Usage.SearchCalls
		d.ImageCalls += r.Usage.ImageCalls
		d.RunCount++
	}
	if days > 0 {
		// Build a continuous window from (today - days + 1) through
		// today, filling in zeros for empty days so the chart renders
		// a proper timeline instead of compressing around active days.
		today := time.Now()
		out := make([]DailyCost, 0, days)
		for i := days - 1; i >= 0; i-- {
			day := today.AddDate(0, 0, -i).Format("2006-01-02")
			if d, ok := daily[day]; ok {
				d.Cost = rates.Estimate(UsageDiff{
					WorkerInput:  d.WorkerInput,
					WorkerOutput: d.WorkerOutput,
					LeadInput:    d.LeadInput,
					LeadOutput:   d.LeadOutput,
					SearchCalls:  d.SearchCalls,
					ImageCalls:   d.ImageCalls,
				})
				out = append(out, *d)
			} else {
				out = append(out, DailyCost{Date: day})
			}
		}
		return out
	}
	out := make([]DailyCost, 0, len(daily))
	for _, d := range daily {
		d.Cost = rates.Estimate(UsageDiff{
			WorkerInput:  d.WorkerInput,
			WorkerOutput: d.WorkerOutput,
			LeadInput:    d.LeadInput,
			LeadOutput:   d.LeadOutput,
			SearchCalls:  d.SearchCalls,
			ImageCalls:   d.ImageCalls,
		})
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// Report logs a multi-line cost report for the given sessions at the
// call-site's defer time. Captures global SearchCalls/ImageCalls at
// call time so the defer can diff them (Session counters cover tokens
// only — searches and image calls don't flow through Session.Chat).
//
// Usage:
//
//	func MyOp() {
//	    worker := agent.CreateSession(WORKER)
//	    lead   := agent.CreateSession(LEAD)
//	    defer Report("my-op", worker, lead)()
//	    // ... work ...
//	}
//
// Sessions must be created before the defer line (they're captured by
// reference). Skips the log when no counters moved so zero-cost paths
// don't spam the log.
//
// NOTE: Report treats sessions as top-level, i.e. their token counts
// starting from creation belong to this operation. For sub-operations
// that share sessions with a parent (a nested op that reuses the
// parent's Worker/Lead), use UsageScope instead — it captures per-
// session starting snapshots so the defer reports the sub-op's delta
// only, not the parent's accumulated total.
//
// The global SearchCalls/ImageCalls delta is taken over Report's full
// window, which includes any nested operations that hit the same
// tracker. For a caller whose own work uses only sessions (no direct
// search/image tools) and who waits on a nested operation, use
// ReportSessions instead — it reports only the sessions' token
// counters and skips the global snapshot.
func Report(label string, sessions ...*Session) func() {
	globalStart := ProcessUsage().Snapshot()
	return func() {
		d := BuildDiff(globalStart, sessions...)
		if d == (UsageDiff{}) {
			return
		}
		Log("%s", FormatUsageReport(label, d))
	}
}

// ReportSessions is the session-only variant of Report: logs the
// aggregate of the given sessions' token counters at defer time
// without touching the global search/image tracker. Use when the
// caller's own work is purely session-scoped LLM calls and the scope
// may wrap a nested operation whose search/image consumption belongs
// to it rather than to this caller (e.g., a scheduler goroutine that
// does topic screening on its own session, then waits on an HTTP-
// triggered pipeline that has its own cost report).
//
// Search and image fields are always zero in the reported UsageDiff.
func ReportSessions(label string, sessions ...*Session) func() {
	return func() {
		var d UsageDiff
		for _, s := range sessions {
			if s == nil {
				continue
			}
			x := s.AsDiff()
			d.WorkerInput += x.WorkerInput
			d.WorkerOutput += x.WorkerOutput
			d.LeadInput += x.LeadInput
			d.LeadOutput += x.LeadOutput
		}
		if d == (UsageDiff{}) {
			return
		}
		Log("%s", FormatUsageReport(label, d))
	}
}

// UsageScope captures baseline snapshots of sessions + the global
// tracker at creation time. Diff() later returns the delta contributed
// between scope creation and the Diff call — works correctly for
// top-level operations (sessions start at zero, snapshot is zero) AND
// for sub-operations that share sessions with a parent (snapshot
// captures the parent's cumulative state so the diff excludes it).
//
// Typical usage (a nested sub-operation that reuses its parent's
// Worker/Lead sessions):
//
//	scope := NewUsageScope(parentWorker, parentLead)
//	defer scope.Report("sub-op-"+id[:8])()
//	// ... nested work runs, reusing the parent's sessions ...
//	// At save site, persist the scope's usage on the sub-op record:
//	record.Usage = scope.Diff()
type UsageScope struct {
	globalStart UsageSnapshot
	sessions    []*Session
	starts      []UsageDiff // tier-split baseline from Session.SnapshotDiff
}

// NewUsageScope captures baselines for the given sessions and the
// process-wide tracker. Call Diff() later (or Report for a logging
// closure) to get the delta contributed since this call.
func NewUsageScope(sessions ...*Session) *UsageScope {
	us := &UsageScope{
		globalStart: ProcessUsage().Snapshot(),
		sessions:    sessions,
		starts:      make([]UsageDiff, len(sessions)),
	}
	for i, s := range sessions {
		if s != nil {
			us.starts[i] = s.SnapshotDiff()
		}
	}
	return us
}

// Diff returns the UsageDiff contributed between scope creation and
// this call. Each session's tier-split counters are diffed against
// their captured baseline — correctly attributes routed/fallback calls
// (e.g. a LEAD session's call that actually ran on the worker counts
// under Worker*, not Lead*). Search/image come from the global tracker.
// Safe to call multiple times; each call reads current state.
func (us *UsageScope) Diff() UsageDiff {
	var d UsageDiff
	for i, s := range us.sessions {
		if s == nil {
			continue
		}
		cur := s.SnapshotDiff()
		start := us.starts[i]
		d.WorkerInput += cur.WorkerInput - start.WorkerInput
		d.WorkerOutput += cur.WorkerOutput - start.WorkerOutput
		d.LeadInput += cur.LeadInput - start.LeadInput
		d.LeadOutput += cur.LeadOutput - start.LeadOutput
	}
	g := ProcessUsage().Diff(us.globalStart)
	d.SearchCalls = g.SearchCalls
	d.ImageCalls = g.ImageCalls
	return d
}

// Report returns a defer-friendly closure that logs the scope's delta
// at call time. Skips the log when no counters moved so zero-cost
// paths don't spam the log.
//
//	scope := NewUsageScope(worker, lead)
//	defer scope.Report("my-op-"+id)()
func (us *UsageScope) Report(label string) func() {
	return func() {
		d := us.Diff()
		if d == (UsageDiff{}) {
			return
		}
		Log("%s", FormatUsageReport(label, d))
	}
}
