package ui

import (
	"encoding/json"
)

// DisplayPanel renders a read-only labeled-value display fetched from
// Source. Optional auto-refresh re-fetches on an interval. Pairs is a
// list of {label, field} entries — the value at record[field] is
// rendered next to label, optionally formatted via Format ("reltime",
// "bytes", "duration"). Actions, when set, render as a button row
// beneath the pairs — same URL templating as Source so {placeholders}
// resolve from the row/page context.
type DisplayPanel struct {
	Source        string          `json:"source"`
	Pairs         []DisplayPair   `json:"pairs"`
	AutoRefreshMS int             `json:"auto_refresh_ms,omitempty"`
	Actions       []ToolbarAction `json:"actions,omitempty"`
}

func (DisplayPanel) componentType() string { return "display_panel" }

func (d DisplayPanel) MarshalJSON() ([]byte, error) {
	type alias DisplayPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"display_panel", alias(d)})
}

// DisplayPair is one labeled value in a DisplayPanel.
type DisplayPair struct {
	Label  string `json:"label"`
	Field  string `json:"field"`
	Format string `json:"format,omitempty"` // "reltime", "bytes", "duration", "" (plain)
	Mono   bool   `json:"mono,omitempty"`   // monospace value (for paths, IDs)
	// Block renders the value as a multi-line <pre> block instead of
	// an inline <span>. Use for long content that needs to preserve
	// newlines and scroll horizontally on overflow — script bodies,
	// pipeline step dumps, full command_templates. Implies Mono.
	Block bool `json:"block,omitempty"`
	// Items, when set, renders the pair's Field as a LIST — the field
	// must resolve to an array. For an array of OBJECTS each element is
	// rendered from these sub-pairs (each sub-pair's Field is looked up
	// on the element, Label prefixes the value); for an array of scalars
	// use a single sub-pair with an empty Field. Generic: any
	// array-of-records field (a toolbox's actions, a pipeline's steps, an
	// agent's allowlist) renders as a readable list instead of the
	// "[object Object]" a plain pair would show. Domain-agnostic — the
	// caller names the sub-fields.
	Items []DisplayPair `json:"items,omitempty"`
	// StatusField names a field on the SAME payload holding "ok", "warn" or
	// "bad", which colours this pair's value. Anything else (including absent)
	// renders plain, so adding it to one pair changes nothing about the others.
	//
	// The SERVER decides the severity, not this component: core/ui has no way
	// to know whether false is good news. A panel reporting "confined: false"
	// in the same grey as everything around it is technically complete and
	// practically invisible, which is the whole reason this exists — the row a
	// reader most needs to notice is exactly the one that looks like every
	// other row.
	StatusField string `json:"status_field,omitempty"`
}

// ChartPanel renders a multi-series chart (bar / line / area / pie) as
// inline SVG. Domain-agnostic: the app (or the Builder via app_def)
// supplies data + a chart type, and the runtime owns the rendering —
// pulling axis/text/grid colors from the active theme so the chart
// follows light/dark automatically, and coloring series from a built-in
// categorical palette. No app names it and no app-specific shape leaks
// in, so it belongs in core/ui.
//
// Two data modes:
//   - Static: set Labels + Series (+ ChartType) inline.
//   - Dynamic: set Source to a JSON endpoint returning
//     {labels, series[, chart_type, title, options]}. The endpoint's
//     fields override the inline ones, so ChartType/Title act as
//     defaults — this is how a source_script-backed custom app chart
//     computes its data from records.
type ChartPanel struct {
	ChartType string        `json:"chart_type,omitempty"` // "bar" | "line" | "area" | "pie"; renderer defaults to bar
	Title     string        `json:"title,omitempty"`
	Labels    []string      `json:"labels,omitempty"`
	Series    []ChartSeries `json:"series,omitempty"`
	Options   *ChartOptions `json:"options,omitempty"`
	Source    string        `json:"source,omitempty"` // JSON endpoint for dynamic data
	// AutoRefreshMS re-fetches Source on an interval, for data that changes on
	// its own rather than when the reader does something.
	// Paused while the tab is hidden and skipped while a fetch is in flight
	// (see uiAutoRefresh); a chart also refreshes on ui-data-changed for its
	// Source, so a record write updates a chart computed from those records.
	AutoRefreshMS int `json:"auto_refresh_ms,omitempty"`
}

// ChartSeries is one series in a ChartPanel. Points feeds bar/line/area
// (one number per Labels entry); Value feeds a pie slice (one series
// per slice). A pie can also be expressed as Labels + a single Points
// series.
type ChartSeries struct {
	Name   string    `json:"name,omitempty"`
	Points []float64 `json:"points,omitempty"`
	Value  *float64  `json:"value,omitempty"`
}

// ChartOptions are optional chart tweaks. Legend defaults to on (nil);
// Stacked applies to bar charts.
type ChartOptions struct {
	Width   int   `json:"width,omitempty"`
	Height  int   `json:"height,omitempty"`
	Stacked bool  `json:"stacked,omitempty"`
	Legend  *bool `json:"legend,omitempty"`
}

func (ChartPanel) componentType() string { return "chart_panel" }

func (c ChartPanel) MarshalJSON() ([]byte, error) {
	type alias ChartPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"chart_panel", alias(c)})
}

// ApiKeyPanel renders a single API-key value with Generate + Copy
// affordances. Source returns the current key as JSON ({key: "..."}
// by default; override with KeyField). GenerateURL is POSTed to
// rotate the key; the response is expected to use the same shape so
// the panel updates in place.
//
// Generic — any "one secret per app" surface can use this:
// blog-suggest public key, phantom bridge key, future webhook
// signing key, etc. The primitive deliberately doesn't take a
// "create" affordance because rotation is the only useful action
// on a single API key.
type ApiKeyPanel struct {
	Source      string `json:"source"`
	GenerateURL string `json:"generate_url,omitempty"` // POST → fresh {key: ...}; empty hides the button
	KeyField    string `json:"key_field,omitempty"`    // default "key"
	// Placeholder shown when the response carries no key (e.g. fresh
	// install before any generation). Defaults to "No key generated".
	Placeholder string `json:"placeholder,omitempty"`
	// ConfirmGenerate — text shown in a confirm() dialog before
	// rotating. Empty disables the prompt; useful for keys where
	// rotation is destructive (invalidates pinned clients).
	ConfirmGenerate string `json:"confirm_generate,omitempty"`
	// AllowCopy adds a Copy-to-clipboard button. On most browsers
	// this needs an HTTPS origin to use the async clipboard API,
	// so the renderer falls back to selectAll+copy when not
	// available.
	AllowCopy bool `json:"allow_copy,omitempty"`
}

func (ApiKeyPanel) componentType() string { return "api_key_panel" }

func (a ApiKeyPanel) MarshalJSON() ([]byte, error) {
	type alias ApiKeyPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"api_key_panel", alias(a)})
}

// BarChart renders a simple SVG bar chart from a JSON array fetched
// from Source. Each entry is one bar; XField labels the X axis,
// YField is the bar height. Fixed-width responsive layout — bars
// share remaining width evenly. Optional Format applies to bar value
// labels (e.g. "$%.4f").
type BarChart struct {
	Source    string `json:"source"`
	XField    string `json:"x_field"`              // label per bar
	YField    string `json:"y_field"`              // numeric height
	YPrefix   string `json:"y_prefix,omitempty"`   // e.g. "$"
	YDecimals int    `json:"y_decimals,omitempty"` // default 2
	HeightPx  int    `json:"height_px,omitempty"`  // default 200
	// MaxWidthPx caps how wide the plot grows inside its section.
	// A bar chart in a full-width section on a wide screen otherwise
	// stretches to thousands of pixels, which reads as distorted
	// rather than informative. Default 900; set 0 for the default,
	// or a large number to opt out of the cap.
	MaxWidthPx int    `json:"max_width_px,omitempty"`
	EmptyText  string `json:"empty_text,omitempty"`
	XFormat    string `json:"x_format,omitempty"` // "date" formats YYYY-MM-DD as "Mon DD"
	// Breakdown adds detail rows to the hover tooltip beyond the
	// headline X/Y. Each pair shows "Label: value" formatted per its
	// Format ("thousands", "reltime", "bytes", "duration", or plain).
	Breakdown []DisplayPair `json:"breakdown,omitempty"`
}

func (BarChart) componentType() string { return "bar_chart" }

func (b BarChart) MarshalJSON() ([]byte, error) {
	type alias BarChart
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"bar_chart", alias(b)})
}
