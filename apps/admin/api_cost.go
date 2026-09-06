package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	. "github.com/cmcoffee/gohort/core"
)

// registerCostRoutes wires the cost API under the admin sub-mux.
func (a *AdminApp) registerCostRoutes(sub *http.ServeMux) {
	// API: cost rates — dollar pricing for per-run LLM + search usage
	// telemetry. Shared between --setup (writes to the same kvlite
	// bucket via core.SaveCostRatesToDB) and this admin page; either
	// path writes the same record and updates live via SetCostRates.
	sub.HandleFunc("/api/cost-rates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.handleGetCostRates(w, r)
		case http.MethodPut:
			a.handleUpdateCostRates(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Per-day cost history for the admin chart. Aggregates across every
	// spend-bearing record type whose package registered a scanner at
	// init time via core.RegisterCostRecordScanner. Apps plug in their
	// own record sources — admin stays generic.
	sub.HandleFunc("/api/cost-history", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleCostHistory(w, r)
	})

	sub.HandleFunc("/api/cost-by-source", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleCostBySource(w, r)
	})

}

// handleGetCostRates returns the currently configured dollar-rate values
// for LLM + search usage telemetry. Rates are stored in the kvlite DB
// under the "cost_rates" bucket by both --setup and this page; the
// per-run log line formats "est. $X.XXXX" using these values. The
// `configured` flag distinguishes "all zeros because never set" from
// "operator explicitly set everything to zero" so the client can
// render blank inputs in the first case and "0" in the second.
func (a *AdminApp) handleGetCostRates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rates := GetCostRates()
	// The multipliers are returned as their EFFECTIVE values rather than
	// the stored ones: both are omitempty, so an unset field would render
	// as a blank box beside a cost that is plainly applying a multiplier.
	// Outer fields shadow the embedded struct's, which is what makes this
	// work without a second type.
	json.NewEncoder(w).Encode(struct {
		CostRates
		CacheReadMultiplier  float64 `json:"cache_read_multiplier"`
		CacheWriteMultiplier float64 `json:"cache_write_multiplier"`
		Configured           bool    `json:"configured"`
	}{rates, rates.EffectiveCacheReadMultiplier(), rates.EffectiveCacheWriteMultiplier(), RatesConfigured()})
}

// handleUpdateCostRates accepts a partial or full CostRates JSON body
// and merges it with the current rates, persisting the result and
// installing it live via SetCostRates. Partial update semantics (each
// field is a pointer) so the form can PUT a single field without
// re-sending the rest.
func (a *AdminApp) handleUpdateCostRates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkerInputPer1K  *float64 `json:"worker_input_per_1k,omitempty"`
		WorkerOutputPer1K *float64 `json:"worker_output_per_1k,omitempty"`
		LeadInputPer1K    *float64 `json:"lead_input_per_1k,omitempty"`
		LeadOutputPer1K   *float64 `json:"lead_output_per_1k,omitempty"`
		SearchPerCall     *float64 `json:"search_per_call,omitempty"`
		ImagePerCall      *float64 `json:"image_per_call,omitempty"`
		// Cached-prompt weights. Not rates in dollars — multipliers on the
		// matching input rate, which is how both providers price them.
		CacheReadMultiplier  *float64 `json:"cache_read_multiplier,omitempty"`
		CacheWriteMultiplier *float64 `json:"cache_write_multiplier,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	rates := GetCostRates()
	current := AuthCurrentUser(r)
	if req.WorkerInputPer1K != nil {
		rates.WorkerInputPer1K = *req.WorkerInputPer1K
		Log("[admin] user %q set worker_input_per_1k=%g", current, *req.WorkerInputPer1K)
	}
	if req.WorkerOutputPer1K != nil {
		rates.WorkerOutputPer1K = *req.WorkerOutputPer1K
		Log("[admin] user %q set worker_output_per_1k=%g", current, *req.WorkerOutputPer1K)
	}
	if req.LeadInputPer1K != nil {
		rates.LeadInputPer1K = *req.LeadInputPer1K
		Log("[admin] user %q set lead_input_per_1k=%g", current, *req.LeadInputPer1K)
	}
	if req.LeadOutputPer1K != nil {
		rates.LeadOutputPer1K = *req.LeadOutputPer1K
		Log("[admin] user %q set lead_output_per_1k=%g", current, *req.LeadOutputPer1K)
	}
	if req.SearchPerCall != nil {
		rates.SearchPerCall = *req.SearchPerCall
		Log("[admin] user %q set search_per_call=%g", current, *req.SearchPerCall)
	}
	if req.ImagePerCall != nil {
		rates.ImagePerCall = *req.ImagePerCall
		Log("[admin] user %q set image_per_call=%g", current, *req.ImagePerCall)
	}
	if req.CacheReadMultiplier != nil {
		rates.CacheReadMultiplier = *req.CacheReadMultiplier
		Log("[admin] user %q set cache_read_multiplier=%g", current, *req.CacheReadMultiplier)
	}
	if req.CacheWriteMultiplier != nil {
		rates.CacheWriteMultiplier = *req.CacheWriteMultiplier
		Log("[admin] user %q set cache_write_multiplier=%g", current, *req.CacheWriteMultiplier)
	}
	if err := SaveCostRatesToDB(a.db, rates); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	SetCostRates(rates)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rates)
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
func (a *AdminApp) handleCostHistory(w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			days = n
		}
	}
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(daily)
}

// handleCostBySource returns each metered source's total spend over the
// window — the admin "Cost by source" breakdown table.
func (a *AdminApp) handleCostBySource(w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			days = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": CostBySource(days)})
}
