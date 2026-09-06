package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// costSections is the cost part of the admin page: Cost History (Last 30 Days), Cost by source, Prices.
func (a *AdminApp) costSections() []ui.Section {
	return []ui.Section{
		// "Worker LLM Thinking" folded into the new "Worker LLM" section above
		// (its thinking + no-think controls now live in that section's
		// collapsible Thinking group). The api/worker-thinking endpoint is
		// retained for compatibility but no longer surfaced here.
		{
			Title:    "Cost History (Last 30 Days)",
			Subtitle: "Daily LLM + search spend across all pipelines. Hover any bar for the per-day breakdown of runs, tokens, searches, and images. The \"in\" figures are the whole prompt; the cached / written rows beneath each are the share of it billed at the cache weights rather than the full input rate.",
			Body: ui.BarChart{
				Source:    costHistorySource,
				XField:    "date",
				YField:    "cost",
				XFormat:   "date",
				YPrefix:   "$",
				YDecimals: 4,
				HeightPx:  220,
				EmptyText: "No usage recorded in the last 30 days.",
				// "in" is the WHOLE prompt (worker_prompt / lead_prompt), not
				// the uncached remainder the provider calls input_tokens —
				// the bar above prices all three components, so showing only
				// the fresh share put a near-zero token count beside a real
				// dollar figure and made the chart look wrong when it wasn't.
				// The cached / written rows below each "in" are the split
				// that explains the price; they omit themselves entirely on a
				// backend that does no caching.
				Breakdown: []ui.DisplayPair{
					{Label: "Runs", Field: "run_count", Format: "thousands"},
					{Label: "Worker in", Field: "worker_prompt", Format: "thousands", Mono: true},
					{Label: "— cached", Field: "worker_cache_read", Format: "thousands", Mono: true},
					{Label: "— written", Field: "worker_cache_write", Format: "thousands", Mono: true},
					{Label: "Worker out", Field: "worker_output", Format: "thousands", Mono: true},
					{Label: "Lead in", Field: "lead_prompt", Format: "thousands", Mono: true},
					{Label: "— cached", Field: "lead_cache_read", Format: "thousands", Mono: true},
					{Label: "— written", Field: "lead_cache_write", Format: "thousands", Mono: true},
					{Label: "Lead out", Field: "lead_output", Format: "thousands", Mono: true},
					{Label: "Searches", Field: "search_calls", Format: "thousands", Mono: true},
					{Label: "Images", Field: "image_calls", Format: "thousands", Mono: true},
				},
			},
		},
		{
			Title:    "Cost by source",
			Subtitle: "Metered source-hook + credential spend over the last 30 days (a \"cost hook\" per source). Set a per-call cost on a source hook or API credential to track it here; it also folds into the chart total above.",
			Body: ui.Table{
				Source:    costBySourceSource,
				RowKey:    "source_id",
				EmptyText: "No metered external calls recorded yet. Set a \"Cost per call\" on a source hook or API credential to track its spend here.",
				Columns: []ui.Col{
					{Field: "label", Label: "Source", Flex: 2},
					{Field: "calls", Label: "Calls", Format: "thousands", Flex: 1},
					{Field: "cost", Label: "Cost ($)", Flex: 1},
				},
			},
		},
		{
			Title:    "Prices",
			Subtitle: "Per-token and per-call dollar rates that feed the dollar estimate above. Worker = local LLM, Lead = remote LLM. Set to 0 for free tiers. Saved automatically as you edit. The two cached-prompt weights at the bottom are multipliers on the input rates, not dollar figures — with prompt caching on, most of a prompt is billed through them rather than at the full input rate.",
			Body: ui.FormPanel{
				Source: "api/cost-rates",
				Method: "PUT",
				Fields: []ui.FormField{
					{Field: "worker_input_per_1k", Label: "Worker input ($/1K tokens)",
						Type: "number", Decimals: 6, Min: 0,
						Help: "Cost of one thousand input tokens to the worker LLM."},
					{Field: "worker_output_per_1k", Label: "Worker output ($/1K tokens)",
						Type: "number", Decimals: 6, Min: 0},
					{Field: "lead_input_per_1k", Label: "Lead input ($/1K tokens)",
						Type: "number", Decimals: 6, Min: 0,
						Help: "Cost of one thousand input tokens to the lead (remote) LLM."},
					{Field: "lead_output_per_1k", Label: "Lead output ($/1K tokens)",
						Type: "number", Decimals: 6, Min: 0},
					{Field: "search_per_call", Label: "Search ($/call)",
						Type: "number", Decimals: 6, Min: 0,
						Help: "Cost per web-search API call (Brave, Tavily, etc.)."},
					{Field: "image_per_call", Label: "Image generation ($/call)",
						Type: "number", Decimals: 6, Min: 0},
					{Field: "cache_read_multiplier", Label: "Cached-read weight (× input rate)",
						Type: "number", Decimals: 4, Min: 0,
						Help: "What a token served FROM cache costs, as a fraction of the input rate above. Anthropic and OpenAI both bill this at 0.1 (a tenth). A long conversation is mostly cache reads, so this is the number that decides whether a 140,000-token prompt reads as expensive or nearly free."},
					{Field: "cache_write_multiplier", Label: "Cache-write weight (× input rate)",
						Type: "number", Decimals: 4, Min: 0,
						Help: "What it costs to WRITE a token into the cache. Anthropic charges 1.25 for the 5-minute TTL and 2.0 for the 1-hour one — set 2.0 if this deployment uses 1-hour caching, or every write is under-reported by 37.5%. Writes are the premium side: a fresh long prompt is nearly all writes, so this drives the cost of first messages rather than follow-ups."},
				},
			},
		},
	}
}
