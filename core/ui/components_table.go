package ui

import (
	"encoding/json"
	"time"
)

// Table renders a list of records fetched from Source. Each row is
// keyed by RowKey; columns project fields out of the record. Rows
// support inline actions (toggle, button, expand) and the table itself
// supports auto-refresh and pull-to-refresh.
type Table struct {
	Source        string      `json:"source"`
	RowKey        string      `json:"row_key"` // primary-key field on each record
	Columns       []Col       `json:"columns"`
	RowActions    []RowAction `json:"row_actions,omitempty"`
	EmptyText     string      `json:"empty_text,omitempty"`
	AutoRefreshMS int         `json:"auto_refresh_ms,omitempty"`
	PullToRefresh bool        `json:"pull_to_refresh,omitempty"`
	SortBy        string      `json:"sort_by,omitempty"` // field to sort by (descending if SortDesc)
	SortDesc      bool        `json:"sort_desc,omitempty"`
	// GroupBy renders rows under headings, one per distinct value of this
	// field, in the order the records arrive (so the server controls grouping
	// order by ordering its rows — no client-side sort to keep in sync). Rows
	// with an empty value render ungrouped, above the first heading. Empty
	// GroupBy = a flat table, the default.
	//
	// Use when one list legitimately holds rows of different KINDS and the kind
	// is what the reader navigates by — e.g. tools split across a user's pool,
	// individual agents, and chat sessions. Prefer a sort or a filter when the
	// rows are the same kind and only differ by value.
	GroupBy string `json:"group_by,omitempty"`
	// Search renders a filter box above the table. Typing narrows rows to those
	// whose visible column values (and group heading) contain the text, matched
	// case-insensitively. Client-side over the rows already fetched — this is a
	// find-in-list affordance, not a server query, so it stays instant and needs
	// no endpoint support.
	//
	// Worth turning on once a list is long enough that a reader scans rather
	// than reads: a tool catalog, a user list. Noise on a short fixed list.
	Search bool `json:"search,omitempty"`
	// SearchPlaceholder overrides the filter box's placeholder text.
	SearchPlaceholder string `json:"search_placeholder,omitempty"`
	// RecordsField extracts the rows from a specific key of the GET
	// response. Use when one endpoint returns multiple lists in a
	// shaped object (e.g. `{pending: [...], active: [...]}`) and you
	// want a Table for each. Empty = auto-detect (top-level array,
	// then `.conversations`, then first-key list).
	RecordsField string `json:"records_field,omitempty"`
	// RowLink names a FIELD on each record holding a destination URL,
	// which makes the whole row navigate there on click. Same shape as
	// Col.Link (a field name, not a template) so the endpoint stays the
	// one place a destination is decided — which matters when reaching it
	// depends on who is asking, since the server can emit the field for
	// viewers who may follow it and leave it empty for those who may not.
	//
	// Rows whose field is empty, or whose value isn't http(s):// or a
	// root-relative path, simply don't link. A row can therefore be
	// clickable or inert per-record without a second table.
	//
	// Clicks on row actions (buttons, toggles, selects, links) are never
	// swallowed; only the row's own dead space navigates. Ctrl/Cmd/middle
	// click opens in a new tab, as with any link.
	RowLink string `json:"row_link,omitempty"`
}

// Refresh sets AutoRefreshMS from a time.Duration for ergonomic Go.
func (t Table) Refresh(d time.Duration) Table {
	t.AutoRefreshMS = int(d / time.Millisecond)
	return t
}

func (Table) componentType() string { return "table" }

func (t Table) MarshalJSON() ([]byte, error) {
	type alias Table
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"table", alias(t)})
}

// Col defines one column in a Table.
//
// Type values:
//   - ""        — plain text (default), formatted via Format
//   - "badge"   — render as a colored badge. Uses Badges to map the
//     record's field value to {label, color}. Ideal for
//     boolean state indicators ("Open" / "Secured",
//     "Enabled" / "Disabled"). Color values: "success",
//     "warning", "danger", "mute".
type Col struct {
	Field  string         `json:"field"`
	Label  string         `json:"label,omitempty"`
	Flex   int            `json:"flex,omitempty"`   // CSS flex weight; 0 = auto
	Format string         `json:"format,omitempty"` // "reltime", "bytes", "thousands", "" (plain)
	Mute   bool           `json:"mute,omitempty"`   // render with --text-mute color
	Type   string         `json:"type,omitempty"`   // "" | "badge" | "dot" | "pills" (array field -> inert chips) | "image" (field holds a URL -> thumbnail)
	Badges []BadgeMapping `json:"badges,omitempty"` // for type="badge" + type="dot" (Label ignored for "dot"; only Color used)
	// Link names another field holding a URL; when set the cell renders as a
	// clickable anchor (text = this column's Field value, href = the Link field's
	// value, opened in a new tab). The framework builds the anchor safely — set
	// this instead of embedding raw <a> HTML in a cell value (which is escaped and
	// shows as literal markup). Only http(s)/relative hrefs render as links.
	Link string `json:"link,omitempty"`
}

// RowAction adds an interactive control to each table row.
//
// Type values:
//   - "toggle" — iOS switch. Field is the boolean field on the record;
//     change POSTs {Field: newValue} to PostTo (with {row_key} substituted).
//   - "select" — inline dropdown. Options must be set; on change POSTs
//     {Field: newValue} (or full record if not PATCH). Use for tables
//     where each row picks from a small enum (e.g. routing tier).
//   - "number" — inline numeric input. Min/Max bound the value; on
//     blur or change POSTs {Field: newValue}.
//   - "button" — labeled button. On click, POSTs (or GETs if Method
//     set) to PostTo. Optional Confirm shows a dialog first.
//     Method "client" instead dispatches to a browser handler
//     registered via window.uiRegisterClientAction — PostTo names
//     the handler, which receives {record, button, reload}.
//   - "expand" — toggleable expansion below the row. Render contains
//     the component shown when expanded; the {row_key} placeholder is
//     substituted into any Source URLs inside.
//   - "modal"  — opens Render in a dialog. Same nested-component +
//     {row_key} substitution as "expand"; the mounted component
//     also receives the row record as ctx plus a __closeModal
//     hook, so a submit-mode FormPanel (Source: per-record GET,
//     PostURL: list upsert endpoint) becomes a prefilled "edit
//     this row" dialog that closes itself on save. Label is the
//     button text and dialog title; Width overrides the dialog
//     max-width (default 520px). Use the ModalAction helper.
type RowAction struct {
	Type    string          `json:"type"`
	Field   string          `json:"field,omitempty"`   // toggle: field on record
	Label   string          `json:"label,omitempty"`   // button + expand: button text
	PostTo  string          `json:"post_to,omitempty"` // toggle + button
	Method  string          `json:"method,omitempty"`  // toggle: PATCH/POST (default POST). button: GET/POST/DELETE (default POST).
	Confirm string          `json:"confirm,omitempty"` // button: confirm() prompt
	Render  json.RawMessage `json:"render,omitempty"`  // expand: nested Component
	// Leading places the action at the FAR LEFT of the row, before the
	// columns. Use for the most-frequently-tapped control (typically a
	// primary toggle) so it's always thumb-reachable on narrow phones,
	// even when long row text would otherwise push it off-screen.
	Leading bool `json:"leading,omitempty"`
	// Compact strips text padding and uses icon-only sizing (≈32px
	// square instead of 44px wide). Use for secondary buttons when the
	// row gets tight on small screens.
	Compact bool `json:"compact,omitempty"`
	// OnlyIf renders the action only when record[field] is truthy.
	// Use for conditional buttons like "Approve" that only apply when
	// a record's `pending` flag is set.
	OnlyIf string `json:"only_if,omitempty"`
	// HideIf is the inverse — render only when record[field] is FALSY.
	// Pair with OnlyIf-on-the-same-field to flip between two actions
	// (e.g. show "Disable" when not disabled, "Enable" when disabled).
	HideIf string `json:"hide_if,omitempty"`
	// Variant styles the action: "danger" colors a button red. Empty
	// uses the default neutral styling.
	Variant string `json:"variant,omitempty"`
	// Options for select-type AND segmented-type row actions. A "segmented"
	// action renders these as a single-select pill track (each option a
	// segment, the one matching record[Field] highlighted) instead of a
	// dropdown — use it for a short, mutually-exclusive ladder the user
	// should see all of at once (e.g. an access-lockdown level). Picking a
	// segment POSTs {Field: value} to PostTo, same as select.
	Options []SelectOption `json:"options,omitempty"`
	// Min, Max for number-type row actions.
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
	// DisableIf hides/disables the control when record[field] is truthy.
	// Use for "private" rows that can't pick certain options (e.g. a
	// private routing stage that can't route to lead).
	DisableIf string `json:"disable_if,omitempty"`
	// FilterOptions removes specific options from a select when
	// record[field] is truthy. Comma-separated list of option values.
	FilterOptionsIf string `json:"filter_options_if,omitempty"`
	FilterOptions   string `json:"filter_options,omitempty"`
	// Width sets the inline control's width (CSS string like "9rem").
	// Use to right-size select/number inputs in dense tables. For
	// modal-type actions it is the dialog max-width (default 520px).
	Width string `json:"width,omitempty"`
	// DefaultField (select-type only) names a field on the record
	// that holds the "default" value for that row. The runtime
	// appends a "*" to the matching option's label so users can
	// see at a glance which choice is the out-of-the-box default,
	// even when they've overridden it. Generic — any per-row select
	// with a per-row default (LLM routing stages, app-specific
	// "default theme" pickers, etc.) can opt in.
	DefaultField string `json:"default_field,omitempty"`
	// RedirectURL (button-type only) — after a successful POST,
	// navigate to this URL instead of reloading the table. Supports
	// {field} placeholders that substitute from the response JSON
	// (e.g. {id} → resp.id), so a "Run" button can route the user
	// to a watch page using the freshly-created session ID.
	// Combine with RedirectTarget to control window placement.
	RedirectURL string `json:"redirect_url,omitempty"`
	// RedirectTarget — "_blank" opens in a new tab (default), "_self"
	// replaces the current page. Only used when RedirectURL is set.
	RedirectTarget string `json:"redirect_target,omitempty"`
	// Optimistic (button-type only) — when true, the row hides
	// immediately on click (before the request fires) and is restored
	// only if the request fails. Default false (table reloads after
	// successful response). Use for delete-style actions where the
	// "thing is gone" feedback is the whole point — the round-trip
	// shouldn't gate the visual.
	Optimistic bool `json:"optimistic,omitempty"`
	// Invalidate — sources to refetch after the action succeeds, for
	// what it changed ELSEWHERE. A row action already reloads its own
	// table and rebroadcasts its source, which covers the common case;
	// this is for the action whose real effect lands somewhere else on
	// the page. Installing from a catalog writes connectors, tools,
	// credentials and skills — every one of them a section of its own,
	// and the point of the install is that you then go and review them.
	Invalidate []string `json:"invalidate,omitempty"`
}

// Expand is a helper to wrap a nested Component for expand-type RowActions.
func Expand(label string, c Component) RowAction {
	return RowAction{
		Type:   "expand",
		Label:  label,
		Render: marshalComponent(c),
	}
}

// ExpandIf is Expand with OnlyIf/HideIf gating on a row field's truthiness —
// e.g. show a Connect expander only while a row is not yet connected. Pass an
// empty string to skip either gate.
func ExpandIf(label, onlyIf, hideIf string, c Component) RowAction {
	a := Expand(label, c)
	a.OnlyIf, a.HideIf = onlyIf, hideIf
	return a
}

// ModalAction wraps a nested Component for modal-type RowActions: a per-row
// button that opens c in a dialog, with the same {row_key} URL substitution
// as Expand. Pair with a submit-mode FormPanel (Source: per-record GET,
// PostURL: list upsert endpoint) for a prefilled "edit this row" dialog —
// the form closes the dialog on a successful save and its Invalidate list
// refreshes the table behind it.
func ModalAction(label string, c Component) RowAction {
	return RowAction{
		Type:   "modal",
		Label:  label,
		Render: marshalComponent(c),
	}
}

// ModalActionIf is ModalAction with OnlyIf/HideIf gating on a row field's
// truthiness (like ExpandIf) — e.g. show a "Request to publish" modal only while a
// row is eligible. Pass an empty string to skip either gate.
func ModalActionIf(label, onlyIf, hideIf string, c Component) RowAction {
	a := ModalAction(label, c)
	a.OnlyIf, a.HideIf = onlyIf, hideIf
	return a
}
