package admin

import (
	"fmt"
	"math"
	"net/http"
	"strconv"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerCostRoutes wires the cost API under the admin sub-mux.
func (a *AdminApp) registerCostRoutes(sub *http.ServeMux) {
	// API: cost rates — dollar pricing for per-run LLM + search usage
	// telemetry. Shared between --setup (writes to the same kvlite
	// bucket via core.SaveCostRatesToDB) and this admin page; either
	// path writes the same record and updates live via SetCostRates.
	sub.HandleFunc("/api/cost-rates", jsonapi.Methods{
		"GET": jsonapi.Get(a.db, a.handleGetCostRates, a.gate()),
		"PUT": jsonapi.Post(a.db, a.handleUpdateCostRates, a.gate()),
	}.Serve)

	// Per-day cost history for the admin chart. Aggregates across every
	// spend-bearing record type whose package registered a scanner at
	// init time via core.RegisterCostRecordScanner. Apps plug in their
	// own record sources — admin stays generic.
	sub.HandleFunc("/api/cost-history", jsonapi.Get(a.db, a.handleCostHistory, a.gate()))

	sub.HandleFunc("/api/cost-by-source", jsonapi.Get(a.db, a.handleCostBySource, a.gate()))

}

// handleGetCostRates returns the currently configured dollar-rate values
// for LLM + search usage telemetry. Rates are stored in the kvlite DB
// under the "cost_rates" bucket by both --setup and this page; the
// per-run log line formats "est. $X.XXXX" using these values. The
// `configured` flag distinguishes "all zeros because never set" from
// "operator explicitly set everything to zero" so the client can
// render blank inputs in the first case and "0" in the second.
func (a *AdminApp) handleGetCostRates(c *jsonapi.Call) (any, error) {
	rates := GetCostRates()
	// The multipliers are returned as their EFFECTIVE values rather than
	// the stored ones: both are omitempty, so an unset field would render
	// as a blank box beside a cost that is plainly applying a multiplier.
	// Outer fields shadow the embedded struct's, which is what makes this
	// work without a second type.
	//
	// Token rates go out per MILLION tokens, which is how every provider
	// publishes them, so a price page's "$1.50 / $9.00" is typed as it
	// reads. They are stored per thousand (CostRates), and only this page
	// sees the other unit.
	return struct {
		CostRates
		CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
		CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
		Configured           bool    `json:"configured"`
		WorkerInputPer1M     float64 `json:"worker_input_per_1m"`
		WorkerOutputPer1M    float64 `json:"worker_output_per_1m"`
		LeadInputPer1M       float64 `json:"lead_input_per_1m"`
		LeadOutputPer1M      float64 `json:"lead_output_per_1m"`
	}{rates, rates.EffectiveCacheReadMultiplier(), rates.EffectiveCacheWriteMultiplier(), RatesConfigured(),
		perMillion(rates.WorkerInputPer1K), perMillion(rates.WorkerOutputPer1K),
		perMillion(rates.LeadInputPer1K), perMillion(rates.LeadOutputPer1K)}, nil
}

// perMillion turns a stored per-thousand rate into the per-million one the
// page shows, rounded so 0.0015 reads 1.5 rather than 1.5000000000000002.
func perMillion(per1K float64) float64 {
	return math.Round(per1K*1000*1e6) / 1e6
}

// handleUpdateCostRates accepts a partial or full CostRates JSON body
// and merges it with the current rates, persisting the result and
// installing it live via SetCostRates. Partial update semantics (each
// field is a pointer) so the form can PUT a single field without
// re-sending the rest.
func (a *AdminApp) handleUpdateCostRates(c *jsonapi.Call, req costRatesUpdate) (any, error) {
	r := c.R
	rates := GetCostRates()
	if req.WorkerInputPer1K != nil {
		rates.WorkerInputPer1K = *req.WorkerInputPer1K
		adminChange(r, activity.Settings, "set worker_input_per_1k=%g", *req.WorkerInputPer1K)
	}
	if req.WorkerOutputPer1K != nil {
		rates.WorkerOutputPer1K = *req.WorkerOutputPer1K
		adminChange(r, activity.Settings, "set worker_output_per_1k=%g", *req.WorkerOutputPer1K)
	}
	if req.LeadInputPer1K != nil {
		rates.LeadInputPer1K = *req.LeadInputPer1K
		adminChange(r, activity.Settings, "set lead_input_per_1k=%g", *req.LeadInputPer1K)
	}
	if req.LeadOutputPer1K != nil {
		rates.LeadOutputPer1K = *req.LeadOutputPer1K
		adminChange(r, activity.Settings, "set lead_output_per_1k=%g", *req.LeadOutputPer1K)
	}
	if req.SearchPerCall != nil {
		rates.SearchPerCall = *req.SearchPerCall
		adminChange(r, activity.Settings, "set search_per_call=%g", *req.SearchPerCall)
	}
	if req.ImagePerCall != nil {
		rates.ImagePerCall = *req.ImagePerCall
		adminChange(r, activity.Settings, "set image_per_call=%g", *req.ImagePerCall)
	}
	if req.CacheReadMultiplier != nil {
		rates.CacheReadMultiplier = *req.CacheReadMultiplier
		adminChange(r, activity.Settings, "set cache_read_multiplier=%g", *req.CacheReadMultiplier)
	}
	if req.CacheWriteMultiplier != nil {
		rates.CacheWriteMultiplier = *req.CacheWriteMultiplier
		adminChange(r, activity.Settings, "set cache_write_multiplier=%g", *req.CacheWriteMultiplier)
	}
	// Applied LAST: the form PUTs the whole record it loaded, which still
	// carries the per-thousand fields, and those must not undo what was
	// just typed per million.
	for _, m := range []struct {
		name string
		in   *float64
		dst  *float64
	}{
		{"worker_input", req.WorkerInputPer1M, &rates.WorkerInputPer1K},
		{"worker_output", req.WorkerOutputPer1M, &rates.WorkerOutputPer1K},
		{"lead_input", req.LeadInputPer1M, &rates.LeadInputPer1K},
		{"lead_output", req.LeadOutputPer1M, &rates.LeadOutputPer1K},
	} {
		if m.in != nil {
			*m.dst = *m.in / 1000
			adminChange(r, activity.Settings, "set %s=$%g per 1M tokens", m.name, *m.in)
		}
	}
	if err := SaveCostRatesToDB(a.db, rates); err != nil {
		return nil, fmt.Errorf("save cost rates: %w", err)
	}
	SetCostRates(rates)
	return rates, nil
}

// costRatesUpdate is the cost-rates form's PUT: any of the rates, each
// optional.
type costRatesUpdate struct {
	WorkerInputPer1K  *float64 `json:"worker_input_per_1k,omitempty"`
	WorkerOutputPer1K *float64 `json:"worker_output_per_1k,omitempty"`
	LeadInputPer1K    *float64 `json:"lead_input_per_1k,omitempty"`
	LeadOutputPer1K   *float64 `json:"lead_output_per_1k,omitempty"`
	// What the page sends: per million tokens, stored per thousand.
	WorkerInputPer1M  *float64 `json:"worker_input_per_1m,omitempty"`
	WorkerOutputPer1M *float64 `json:"worker_output_per_1m,omitempty"`
	LeadInputPer1M    *float64 `json:"lead_input_per_1m,omitempty"`
	LeadOutputPer1M   *float64 `json:"lead_output_per_1m,omitempty"`
	SearchPerCall     *float64 `json:"search_per_call,omitempty"`
	ImagePerCall      *float64 `json:"image_per_call,omitempty"`
	// Cached-prompt weights. Not rates in dollars — multipliers on the
	// matching input rate, which is how both providers price them.
	CacheReadMultiplier  *float64 `json:"cache_read_multiplier,omitempty"`
	CacheWriteMultiplier *float64 `json:"cache_write_multiplier,omitempty"`
}

// handleCostHistory returns per-day cost aggregation across every
// spend-bearing record type whose package registered a scanner via
// core.RegisterCostRecordScanner. Scanner authors are responsible for
// avoiding double-counting (e.g., skipping records whose Usage is
// already included in a parent record's totals).
//
// Query params:
//
//	days=<n>  trailing window ending today (default 30; 0 = all data)
//
// The chart consumes this directly: each DailyCost row prices the
// day's usage at current CostRates, so rate changes propagate
// immediately without re-scanning.
func (a *AdminApp) handleCostHistory(c *jsonapi.Call) (any, error) {
	days := costDays(c)
	records := CollectAllUsage()
	daily := AggregateDailyCost(records, days)
	// Fold metered source-hook / credential spend (cost hooks) into each day's
	// total. A day that had ONLY external spend (no LLM usage) won't have a row
	// from AggregateDailyCost, so append one for it.
	ext := CostExternalDaily(days)
	seen := map[string]int{}
	for i := range daily {
		seen[daily[i].Date] = i
	}
	for date, cost := range ext {
		if i, ok := seen[date]; ok {
			daily[i].ExternalCost = cost
			daily[i].Cost += cost
		} else {
			daily = append(daily, DailyCost{Date: date, Cost: cost, ExternalCost: cost})
		}
	}
	return daily, nil
}

// handleCostBySource returns each metered source's total spend over the
// window — the admin "Cost by source" breakdown table.
func (a *AdminApp) handleCostBySource(c *jsonapi.Call) (any, error) {
	return map[string]any{"items": CostBySource(costDays(c))}, nil
}

// costDays is the ?days= window: 30 unless it names another, 0 for all data.
func costDays(c *jsonapi.Call) int {
	if n, err := strconv.Atoi(c.R.URL.Query().Get("days")); err == nil && n >= 0 {
		return n
	}
	return 30
}
