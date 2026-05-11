package ui

import (
	"encoding/json"
	"time"
)

// Each component MarshalJSON wraps its own struct with a `type` tag the
// runtime dispatches on. This keeps Go-side construction ergonomic
// (plain field-by-field literals) while emitting tagged JSON.

// PanicBar is a sticky-top button typically used for "disable
// everything" emergency actions. The button POSTs to OnClick and
// expects a JSON response (any shape — surfaced as a status line).
type PanicBar struct {
	Label   string `json:"label"`
	OnClick string `json:"on_click"`
	Confirm string `json:"confirm,omitempty"` // confirm() prompt before firing
}

func (PanicBar) componentType() string { return "panic_bar" }
func (p PanicBar) MarshalJSON() ([]byte, error) {
	type alias PanicBar
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"panic_bar", alias(p)})
}

// ToggleGroup renders a list of iOS-style switches bound to fields
// from a single JSON document (Source URL). Each toggle's change POSTs
// the entire updated document back to Source — server is the source of
// truth, the runtime just round-trips.
type ToggleGroup struct {
	Source  string   `json:"source"`           // GET + POST endpoint
	Toggles []Toggle `json:"toggles"`
}

func (ToggleGroup) componentType() string { return "toggle_group" }
func (g ToggleGroup) MarshalJSON() ([]byte, error) {
	type alias ToggleGroup
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"toggle_group", alias(g)})
}

// Toggle is a single switch within a ToggleGroup.
type Toggle struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// MemberEditor renders an editable list of {handle, name, aliases}
// records bound to one field of a parent record (Source URL). Each
// row has an inline handle input, name input, and a comma-separated
// aliases input; rows can be added or removed, and saves PATCH the
// full updated array back to PostTo on blur. Used by phantom's
// per-conversation members editor for group chats.
type MemberEditor struct {
	Source        string `json:"source"`
	PostTo        string `json:"post_to"`
	Method        string `json:"method,omitempty"`         // default POST
	Field         string `json:"field,omitempty"`          // default "members"
	HandleField   string `json:"handle_field,omitempty"`   // default "handle"
	NameField     string `json:"name_field,omitempty"`     // default "name"
	AliasesField  string `json:"aliases_field,omitempty"`  // default "aliases"
	AliasHandlesField string `json:"alias_handles_field,omitempty"` // optional sibling field on the parent record (e.g. "alias_handles") — comma-sep textbox
	EmptyText     string `json:"empty_text,omitempty"`
}

func (MemberEditor) componentType() string { return "member_editor" }
func (m MemberEditor) MarshalJSON() ([]byte, error) {
	type alias MemberEditor
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"member_editor", alias(m)})
}

// KeyManager renders a "list + create-once-show-secret + delete"
// flow for API keys (or any other one-shot reveal credential). On
// create, the server's response is expected to include the full
// secret on a `key` (or SecretField) field; the UI shows it once
// inside the panel with a Copy button, then refreshes the list to
// drop it. Stored rows show only metadata — no secret round-trips
// back to the client.
//
// ListURL    GET → array of {ID, Name, Created, LastSeen, ...}
// CreateURL  POST {name: "..."} → {ID, Name, Key, Created, ...}
// DeleteURL  DELETE — id is appended to the URL
type KeyManager struct {
	ListURL     string `json:"list_url"`
	CreateURL   string `json:"create_url"`
	DeleteURL   string `json:"delete_url"`
	NameField   string `json:"name_field,omitempty"`   // default "name"
	IDField     string `json:"id_field,omitempty"`     // default "id"
	SecretField string `json:"secret_field,omitempty"` // default "key"
	CreatedField string `json:"created_field,omitempty"` // default "created"
	LastSeenField string `json:"last_seen_field,omitempty"` // default "last_seen"
	NewLabel    string `json:"new_label,omitempty"`    // default "+ New API key"
	EmptyText   string `json:"empty_text,omitempty"`
	// SecretHint is the helper text shown next to the freshly-revealed
	// secret. Use to remind the user that this is the only chance to
	// copy it (per-app phrasing — "use in the macOS bridge config" /
	// etc).
	SecretHint string `json:"secret_hint,omitempty"`
}

func (KeyManager) componentType() string { return "key_manager" }
func (k KeyManager) MarshalJSON() ([]byte, error) {
	type alias KeyManager
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"key_manager", alias(k)})
}

// Table renders a list of records fetched from Source. Each row is
// keyed by RowKey; columns project fields out of the record. Rows
// support inline actions (toggle, button, expand) and the table itself
// supports auto-refresh and pull-to-refresh.
type Table struct {
	Source        string           `json:"source"`
	RowKey        string           `json:"row_key"`           // primary-key field on each record
	Columns       []Col            `json:"columns"`
	RowActions    []RowAction      `json:"row_actions,omitempty"`
	EmptyText     string           `json:"empty_text,omitempty"`
	AutoRefreshMS int              `json:"auto_refresh_ms,omitempty"`
	PullToRefresh bool             `json:"pull_to_refresh,omitempty"`
	SortBy        string           `json:"sort_by,omitempty"`     // field to sort by (descending if SortDesc)
	SortDesc      bool             `json:"sort_desc,omitempty"`
	// RecordsField extracts the rows from a specific key of the GET
	// response. Use when one endpoint returns multiple lists in a
	// shaped object (e.g. `{pending: [...], active: [...]}`) and you
	// want a Table for each. Empty = auto-detect (top-level array,
	// then `.conversations`, then first-key list).
	RecordsField  string           `json:"records_field,omitempty"`
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
//                 record's field value to {label, color}. Ideal for
//                 boolean state indicators ("Open" / "Secured",
//                 "Enabled" / "Disabled"). Color values: "success",
//                 "warning", "danger", "mute".
type Col struct {
	Field  string         `json:"field"`
	Label  string         `json:"label,omitempty"`
	Flex   int            `json:"flex,omitempty"`   // CSS flex weight; 0 = auto
	Format string         `json:"format,omitempty"` // "reltime", "bytes", "thousands", "" (plain)
	Mute   bool           `json:"mute,omitempty"`   // render with --text-mute color
	Type   string         `json:"type,omitempty"`   // "" or "badge"
	Badges []BadgeMapping `json:"badges,omitempty"` // for type="badge"
}

// BadgeMapping maps a value (typically a boolean) to a labeled badge
// for the "badge" Col type. The first match (by deep equality on
// Value) wins; if nothing matches, the field value is rendered with
// the "mute" color and no specific label.
type BadgeMapping struct {
	Value any    `json:"value"`           // matched against the record's field value
	Label string `json:"label"`           // shown inside the badge
	Color string `json:"color,omitempty"` // "success", "warning", "danger", "mute"
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
//   - "expand" — toggleable expansion below the row. Render contains
//     the component shown when expanded; the {row_key} placeholder is
//     substituted into any Source URLs inside.
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
	// Options for select-type row actions.
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
	// Use to right-size select/number inputs in dense tables.
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
}

// Expand is a helper to wrap a nested Component for expand-type RowActions.
func Expand(label string, c Component) RowAction {
	return RowAction{
		Type:   "expand",
		Label:  label,
		Render: marshalComponent(c),
	}
}

// HistoryPanel renders a scrollable list of messages fetched from
// Source. Used inside RowAction Expand for chat-history-style displays.
type HistoryPanel struct {
	Source       string `json:"source"`
	Header       string `json:"header,omitempty"`
	EmptyText    string `json:"empty_text,omitempty"`
	MaxMessages  int    `json:"max_messages,omitempty"` // trim before render; 0 = unlimited
	RoleField    string `json:"role_field,omitempty"`   // default "role"
	TextField    string `json:"text_field,omitempty"`   // default "text"
	WhoField     string `json:"who_field,omitempty"`    // default "display_name"
	TimeField    string `json:"time_field,omitempty"`   // default "timestamp"
	AssistantTag string `json:"assistant_tag,omitempty"` // role value that means "AI"; default "assistant"
}

func (HistoryPanel) componentType() string { return "history_panel" }
func (h HistoryPanel) MarshalJSON() ([]byte, error) {
	type alias HistoryPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"history_panel", alias(h)})
}

// ChipPicker renders a wrapping row of selectable chips. The selection
// is bound to a field on a parent record (used inside expand actions
// for things like per-conversation tool selection).
type ChipPicker struct {
	OptionsSource string `json:"options_source"` // GET — array of options
	RecordSource  string `json:"record_source"`  // GET — current record
	Field         string `json:"field"`          // array field on record
	PostTo        string `json:"post_to"`        // destination for updated record
	Method        string `json:"method,omitempty"` // default POST; use PATCH for partial-update endpoints
	// NameField is the option key whose value gets STORED in the
	// record's selection array (e.g. "/phantom" path string).
	// Default "name".
	NameField string `json:"name_field,omitempty"`
	// LabelField is the option key whose value is rendered as the
	// chip's visible text. When unset, the chip shows NameField.
	// Use this when the storage value isn't human-readable (e.g.
	// store a URL path but display the app's friendly name).
	LabelField string `json:"label_field,omitempty"`
	// DescField is the option key for tooltip text. Default "desc".
	DescField string `json:"desc_field,omitempty"`
}

func (ChipPicker) componentType() string { return "chip_picker" }
func (c ChipPicker) MarshalJSON() ([]byte, error) {
	type alias ChipPicker
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"chip_picker", alias(c)})
}

// FormPanel renders a list of labeled input fields bound to a single
// JSON record (Source URL). Each field's change saves the full record
// back to Source. Text/textarea fields are debounced so we don't POST
// on every keystroke.
//
// Method defaults to POST. Use "PATCH" + only-the-changed-field saving
// for endpoints that don't accept full-record overwrites.
type FormPanel struct {
	Source string      `json:"source"`
	Method string      `json:"method,omitempty"`
	Fields []FormField `json:"fields"`
	// PostURL — destination for saves when it differs from Source.
	// Defaults to Source. Use when the GET endpoint that returns the
	// current record shape isn't the right write target — e.g. an
	// edit form whose Source is `api/record/{id}` (returns the row)
	// but whose saves go to `api/records` (the list endpoint that
	// handles both create and update via the ID field on the body).
	PostURL string `json:"post_url,omitempty"`
}

func (FormPanel) componentType() string { return "form_panel" }
func (f FormPanel) MarshalJSON() ([]byte, error) {
	type alias FormPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"form_panel", alias(f)})
}

// FormField describes one input in a FormPanel.
//
// Type values:
//   - "text"     — single-line text input (default)
//   - "textarea" — multi-line text input; Rows controls height
//   - "number"   — numeric input with Min/Max bounds
//   - "select"   — dropdown with Options
//   - "tel"      — phone number (better mobile keyboard)
//   - "rules"    — line-separated list editor (one input per row,
//                  saves as a newline-joined string)
//   - "tags"     — compact chip array editor (saves as a JSON
//                  string array), suited for keyword-style fields
//   - "toggle"   — iOS-style switch bound to a boolean field
//   - "password" — masked input (renders as <input type="password">).
//                  Pair with a server convention where GET returns
//                  the placeholder "(configured)" for an existing
//                  secret and POST only updates when the field
//                  differs from that placeholder; otherwise the user
//                  re-saving the form would overwrite the stored
//                  secret with the placeholder.
type FormField struct {
	Field       string         `json:"field"`
	Label       string         `json:"label,omitempty"`
	Type        string         `json:"type,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	Help        string         `json:"help,omitempty"`
	Rows        int            `json:"rows,omitempty"`
	Min         int            `json:"min,omitempty"`
	Max         int            `json:"max,omitempty"`
	// Decimals enables float input on a "number" field. 0 = integer
	// only (default). >0 = parseFloat with that many decimal places
	// in the saved value (use 4 for per-1K-token rates like 0.0003).
	Decimals    int            `json:"decimals,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	// ShowWhen names another field in the same FormPanel; this field
	// is rendered (and saves are wired) only when that field's current
	// value is truthy. Use to collapse irrelevant configuration when a
	// master toggle is off — e.g. hide whisper/piper URLs until
	// `enabled` is on. Updates immediately when the gating field changes.
	ShowWhen string `json:"show_when,omitempty"`

	// Chips — when ChipsSource is set, the field renders with a row
	// of clickable preset chips above the input. Each chip applies a
	// preset value to the field. Designed for things like persona
	// pickers where users want fast access to saved presets plus
	// optional AI-assisted creation of new ones.
	ChipsSource     string `json:"chips_source,omitempty"`     // GET → [{id, name, <value-field>, builtin?}]
	ChipsValueField string `json:"chips_value_field,omitempty"` // field on each chip whose value goes into the input (default "value")
	// ChipsCreate enables "+ New" affordance. POSTed body =
	// {name, <value-field>: "..."}. Server returns updated list.
	ChipsCreateURL  string `json:"chips_create_url,omitempty"`
	// ChipsDeleteURL deletes a custom chip. {id} substituted at click.
	// Only fires for non-builtin chips (double-click to delete).
	ChipsDeleteURL  string `json:"chips_delete_url,omitempty"`
	// ChipsAssistURL takes a seed (POST {seed}) and returns plain
	// text — used for the "AI Assist" button inside the create dialog.
	ChipsAssistURL  string `json:"chips_assist_url,omitempty"`
	// ChipsAddLabel — text on the "+ New" chip; defaults to "+ New".
	ChipsAddLabel   string `json:"chips_add_label,omitempty"`
	// ChipsAlsoSet — additional form fields to populate when a chip
	// is picked. Map keys name target form fields; values name the
	// property on the chip record to read. Example: persona chips
	// carry both {personality, name}; the personality chip-picker
	// declares ChipsAlsoSet: {"persona_name": "name"} so picking a
	// persona auto-fills the separate Persona name field with the
	// persona's name. Generic — any chip with multiple useful
	// fields can fan them out into companion form inputs.
	ChipsAlsoSet map[string]string `json:"chips_also_set,omitempty"`
}

// SelectOption is one entry in a "select" FormField.
type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// DisplayPanel renders a read-only labeled-value display fetched from
// Source. Optional auto-refresh re-fetches on an interval. Pairs is a
// list of {label, field} entries — the value at record[field] is
// rendered next to label, optionally formatted via Format ("reltime",
// "bytes", "duration").
type DisplayPanel struct {
	Source        string         `json:"source"`
	Pairs         []DisplayPair  `json:"pairs"`
	AutoRefreshMS int            `json:"auto_refresh_ms,omitempty"`
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

// PipelineWatchPanel is a live "follow a long-running pipeline" view.
// Mirrors the legacy /watch page shape (header + stage bar + status
// feed + final article display + completion actions) but configurable
// per app.
//
// Wire-up:
//   - InfoURL is fetched once on page load. Returns the initial
//     record ({topic, status, done}). Used to seed the header before
//     SSE events arrive.
//   - EventsURL is the SSE stream. Each event payload is JSON
//     {stage, message, ...} — the panel dispatches on `stage` to
//     update pills, append status, or render the final article.
//   - CancelURL is the POST destination for the Cancel button.
//
// Stage handling:
//   - Stages declares the stage pills (in order) shown across the
//     top bar. Each stage advances to "done" when a later stage
//     becomes active, or "error" when the configured ErrorStage
//     fires.
//   - A stage with a SubPattern regex creates dynamic sub-pills off
//     the message text — used to fan out "research" into per-gap /
//     per-angle pills the way legacy did. SubLabelTemplate defaults
//     to "$1".
//
// Special-cased stages:
//   - ArticleStage signals "show the final rendered markdown body
//     instead of the status feed". The event payload should carry
//     {title, content}.
//   - DraftStage shows a collapsible "rough draft" panel with the
//     event's message body rendered as markdown. Optional.
//   - DoneStage marks the pipeline complete (defaults to "done"
//     and "stream_end"). After firing, OnDoneActions render as a
//     row of buttons under the article/status view.
//   - ErrorStage marks the pipeline failed (default "error").
type PipelineWatchPanel struct {
	InfoURL    string `json:"info_url"`
	EventsURL  string `json:"events_url"`
	CancelURL  string `json:"cancel_url,omitempty"`
	AppName    string `json:"app_name,omitempty"`
	TopicField string `json:"topic_field,omitempty"` // default "topic"
	DoneField  string `json:"done_field,omitempty"`  // default "done"
	Stages     []WatchStage `json:"stages,omitempty"`
	// Stage names that get special treatment. Empty values fall back
	// to the documented defaults so the simplest config can omit them.
	ArticleStage string `json:"article_stage,omitempty"` // default "article"
	DraftStage   string `json:"draft_stage,omitempty"`   // default "rough_draft"
	DoneStage    string `json:"done_stage,omitempty"`    // default "done"
	ErrorStage   string `json:"error_stage,omitempty"`   // default "error"
	// OnDoneActions render as a row of buttons under the article/
	// status view once the pipeline completes. Each action's URL
	// can substitute {field} placeholders from the final event data.
	OnDoneActions []WatchAction `json:"on_done_actions,omitempty"`
}

// WatchStage describes one stage pill in PipelineWatchPanel's top bar.
type WatchStage struct {
	Key              string `json:"key"`               // matches data.stage
	Label            string `json:"label,omitempty"`   // visible text; defaults to capitalized Key
	Icon             string `json:"icon,omitempty"`    // emoji prefix
	SubPattern       string `json:"sub_pattern,omitempty"` // regex on data.message — match → spawn sub-pill
	SubLabelTemplate string `json:"sub_label_template,omitempty"` // default "$1"
}

// WatchAction is one button shown after the pipeline completes.
type WatchAction struct {
	Label   string `json:"label"`
	URL     string `json:"url"`
	Method  string `json:"method,omitempty"`  // "GET" (default, renders as link), "POST"
	Variant string `json:"variant,omitempty"` // "primary", "danger", or empty
	NewTab  bool   `json:"new_tab,omitempty"` // GET-mode only; opens in new tab
}

func (PipelineWatchPanel) componentType() string { return "pipeline_watch_panel" }
func (p PipelineWatchPanel) MarshalJSON() ([]byte, error) {
	type alias PipelineWatchPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"pipeline_watch_panel", alias(p)})
}

// SuggestPanel renders an LLM-backed suggestion list with an optional
// "focus" / direction text input and a trigger button. On click, the
// panel POSTs to URL with the direction body and renders the returned
// array as a clickable list. Each item can fire two actions:
//
//   - PrimaryAction — the click action on the row body. Typically used
//     to "pick" the suggestion into a target form field, or POST the
//     row directly to a queue/destination endpoint.
//   - SecondaryAction — optional per-row button shown on the right
//     (e.g. "+ Queue"). Independent of the row's primary click.
//
// QuestionField / HookField name the keys on each returned item to
// render as the title and muted-second-line hook respectively.
type SuggestPanel struct {
	URL            string `json:"url"`             // POST returns []{question, hook}
	Method         string `json:"method,omitempty"` // default POST
	DirectionField string `json:"direction_field,omitempty"` // body field name for the input value (default "direction")
	Placeholder    string `json:"placeholder,omitempty"`
	SuggestLabel   string `json:"suggest_label,omitempty"`   // default "Suggest"
	// QuestionField — key on each list item rendered as the topic.
	// Defaults to "question". Falls through to "topic" or "text" if
	// the field is missing on the response.
	QuestionField string `json:"question_field,omitempty"`
	// HookField — key on each list item shown as the muted second
	// line. Defaults to "hook". Falls through to "description" or
	// "summary".
	HookField string `json:"hook_field,omitempty"`
	// PrimaryAction — fires when the row body is clicked.
	PrimaryAction *SuggestAction `json:"primary_action,omitempty"`
	// SecondaryAction — fires when the optional per-row button is
	// clicked. Renders only when set.
	SecondaryAction *SuggestAction `json:"secondary_action,omitempty"`
	// EmptyText — message shown after a Suggest click that returns
	// an empty array. Defaults to "No suggestions returned."
	EmptyText string `json:"empty_text,omitempty"`
}

// SuggestAction describes either the click-the-row action or the
// optional secondary per-row button on a SuggestPanel.
//
//   - URL is the POST destination ({question}, {hook}, etc. fields
//     get substituted from the row item, plus {direction} from the
//     panel's input).
//   - BodyFields lists the row keys to forward as the request body
//     ({topic: row.question, ...}). Empty BodyFields = empty body.
//   - Confirm displays a confirm() dialog before firing.
//   - Label is shown on the button (only used by SecondaryAction;
//     PrimaryAction has no visible label of its own).
type SuggestAction struct {
	Label   string `json:"label,omitempty"`
	URL     string `json:"url,omitempty"`
	Method  string `json:"method,omitempty"` // default POST
	Confirm string `json:"confirm,omitempty"`
	// BodyMap declares how to build the request body. Keys are the
	// destination JSON-body field names; values name the source
	// field on the suggestion item ({"topic": "question"} sends
	// {topic: row.question}). Empty BodyMap = no request body.
	BodyMap map[string]string `json:"body_map,omitempty"`
	// Toast — message shown in the bottom toast on success. Variables
	// {question}/{hook} get substituted from the row.
	Toast string `json:"toast,omitempty"`
	// Invalidate — list of data-source URLs to invalidate after a
	// successful action. Tables (and any future list components)
	// listening for ui-data-changed events on these sources will
	// reload. Use to refresh a sibling table when this action
	// queues / files / saves into it (e.g. SuggestPanel's "Queue"
	// action should invalidate "api/queue" so the Blog Queue table
	// reflects the new item without a page reload).
	Invalidate []string `json:"invalidate,omitempty"`
}

func (SuggestPanel) componentType() string { return "suggest_panel" }
func (s SuggestPanel) MarshalJSON() ([]byte, error) {
	type alias SuggestPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"suggest_panel", alias(s)})
}

// BarChart renders a simple SVG bar chart from a JSON array fetched
// from Source. Each entry is one bar; XField labels the X axis,
// YField is the bar height. Fixed-width responsive layout — bars
// share remaining width evenly. Optional Format applies to bar value
// labels (e.g. "$%.4f").
type BarChart struct {
	Source     string `json:"source"`
	XField     string `json:"x_field"`     // label per bar
	YField     string `json:"y_field"`     // numeric height
	YPrefix    string `json:"y_prefix,omitempty"`    // e.g. "$"
	YDecimals  int    `json:"y_decimals,omitempty"`  // default 2
	HeightPx   int    `json:"height_px,omitempty"`   // default 200
	EmptyText  string `json:"empty_text,omitempty"`
	XFormat    string `json:"x_format,omitempty"`    // "date" formats YYYY-MM-DD as "Mon DD"
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

// ActionList renders a list of buttons that fire one-shot side-effect
// actions. Useful for "maintenance" panels (purge cache, reindex, etc.)
// where each entry is a labeled action with a brief description.
//
// Source returns a JSON array of items; each item supplies the button's
// label, description, and the substitution context for PostTo. The
// runtime substitutes {key}-style placeholders in PostTo from each
// item before firing the request.
type ActionList struct {
	Source     string `json:"source"`
	LabelField string `json:"label_field,omitempty"`  // default "Label"
	DescField  string `json:"desc_field,omitempty"`   // default "Desc"
	PostTo     string `json:"post_to"`                // e.g. "api/maintenance?key={Label}"
	Method     string `json:"method,omitempty"`       // default POST
	Confirm    string `json:"confirm,omitempty"`
	ButtonText string `json:"button_text,omitempty"`  // default "Run"
	EmptyText  string `json:"empty_text,omitempty"`
}

func (ActionList) componentType() string { return "action_list" }
func (a ActionList) MarshalJSON() ([]byte, error) {
	type alias ActionList
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"action_list", alias(a)})
}

// JSONView renders a field's value as pretty-printed JSON inside a
// scrollable monospace block. Designed for expand panels where the
// row record carries opaque structured data (e.g. a scheduled task's
// payload). When mounted inside an Expand, the record context comes
// from the parent row — no fetch required.
type JSONView struct {
	Field string `json:"field"`            // field on the parent record
	Title string `json:"title,omitempty"`  // optional header above the block
}

func (JSONView) componentType() string { return "json_view" }
func (j JSONView) MarshalJSON() ([]byte, error) {
	type alias JSONView
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"json_view", alias(j)})
}

// RecordView is a labeled list of fields drawn from the parent record
// (when used inside an Expand) or from a fetched Source. Same shape as
// DisplayPanel but reads from row context instead of a separate URL.
type RecordView struct {
	Source string        `json:"source,omitempty"` // optional GET; falls back to expand ctx
	Pairs  []DisplayPair `json:"pairs"`
}

func (RecordView) componentType() string { return "record_view" }
func (r RecordView) MarshalJSON() ([]byte, error) {
	type alias RecordView
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"record_view", alias(r)})
}

// Stack renders multiple components vertically inside one container —
// useful for Expand panels that want to show both a RecordView and a
// JSONView, or any other combination.
type Stack struct {
	Children []Component `json:"-"`
	Items    []json.RawMessage `json:"items"`
}

func (Stack) componentType() string { return "stack" }
func (s Stack) MarshalJSON() ([]byte, error) {
	if s.Items == nil && s.Children != nil {
		s.Items = make([]json.RawMessage, len(s.Children))
		for i, c := range s.Children {
			s.Items[i] = marshalComponent(c)
		}
	}
	return json.Marshal(struct {
		Type  string            `json:"type"`
		Items []json.RawMessage `json:"items"`
	}{"stack", s.Items})
}

// ChatPanel renders a streaming-chat layout: sessions sidebar, message
// thread, and an input area. The runtime handles SSE event parsing
// (chunk / tool_call / tool_result / done / error / session / status),
// markdown rendering of completed messages, and per-message Copy/Speak
// actions. Designed as the generic conversation primitive.
//
// Session APIs (all required):
//   - SessionsListURL: GET → array of session summaries with the
//     fields named by SessionIDField, SessionTitleField,
//     SessionLastAtField. Default field names match chat's existing
//     ChatSession schema (capitalized).
//   - SessionLoadURL: GET with {id} → full session including messages.
//   - SessionDeleteURL: DELETE with {id}.
//
// Send endpoint:
//   - SendURL: POST → SSE response. The runtime POSTs JSON
//     {session_id, message, history, ...ExtraSendFields}. Each SSE
//     event is parsed and dispatched: chunk events stream into the
//     latest assistant bubble; tool_call/tool_result render inline
//     pills; session events update the active session id; done/error
//     finalize the round.
type ChatPanel struct {
	SessionsListURL  string `json:"sessions_list_url"`
	SessionLoadURL   string `json:"session_load_url"`
	SessionDeleteURL string `json:"session_delete_url"`
	SendURL          string `json:"send_url"`
	// CancelURL aborts the in-flight server-side stream when set.
	// POST → 204 expected; runtime fires alongside the client-side
	// AbortController so server-side resources release immediately.
	CancelURL string `json:"cancel_url,omitempty"`
	// Field names for the session summary records — defaults match
	// chat's ChatSession schema. Override only when migrating an app
	// with a different shape.
	SessionIDField      string `json:"session_id_field,omitempty"`      // default "ID"
	SessionTitleField   string `json:"session_title_field,omitempty"`   // default "Title"
	SessionLastAtField  string `json:"session_last_at_field,omitempty"` // default "LastAt"
	// Field on the loaded session that holds the messages array.
	// Default "Messages".
	SessionMessagesField string `json:"session_messages_field,omitempty"`
	// Empty-state copy.
	EmptyText string `json:"empty_text,omitempty"`
	// Modes are toggle pills shown above the input. Each binds to a
	// pair of GET/POST URLs that round-trip a single boolean. The
	// runtime mixes the toggle's current value into the send body so
	// the server sees the active modes.
	Modes []ChatMode `json:"modes,omitempty"`
	// Voice enables mic (push-to-talk transcribe), Speak per-message,
	// auto-speak, and convo-mode toggle. The runtime probes
	// /voice/status to decide which controls to show.
	Voice bool `json:"voice,omitempty"`
	// BulkSelect adds checkboxes to each session in the sidebar and a
	// bulk-actions bar at the top of the list. Currently supports
	// bulk delete via SessionDeleteURL repeated per selection.
	BulkSelect bool `json:"bulk_select,omitempty"`
	// Attachments enables a 📎 button + paste/drag attach for plain-
	// text files (logs, configs). The text is appended to the next
	// outgoing message in a fenced code block.
	Attachments bool `json:"attachments,omitempty"`
	// Markdown enables rendering of completed assistant messages
	// through a small built-in markdown renderer (headings, code
	// fences, inline code, bold, italic, links, lists). Streaming
	// chunks render plain until the round's done event arrives.
	Markdown bool `json:"markdown,omitempty"`
	// ExtraFields are arbitrary form fields rendered in the modes
	// bar (alongside Private/Explorer toggles). Their current values
	// ride along on every send body so the server sees them as
	// session parameters. Used by debate for the rounds picker.
	ExtraFields []ChatField `json:"extra_fields,omitempty"`
	// PrefillURL is an optional GET endpoint that returns plain text
	// to drop into the message input. When set, a small button is
	// rendered next to the send button that triggers the fetch.
	// Debate uses this for the "Suggest a topic" button.
	PrefillURL   string `json:"prefill_url,omitempty"`
	PrefillLabel string `json:"prefill_label,omitempty"` // default "Suggest"
	// SessionArchiveURL — when set, each session in the sidebar gets
	// an Archive toggle action. POSTed with the session id; server
	// flips the archive flag and returns the new state.
	SessionArchiveURL string `json:"session_archive_url,omitempty"`
	// ToolsURL — when set, the modes bar shows an expandable
	// "N tools" badge that fetches the URL on first open. Server
	// returns a JSON array of {name, desc}. Useful for showing the
	// LLM's available tool catalog at a glance.
	ToolsURL string `json:"tools_url,omitempty"`
}

// ChatField defines one extra form field rendered in the chat
// toolbar. Only the bare minimum types — number, select, text —
// are supported; anything richer should ride on a Mode toggle.
type ChatField struct {
	Name    string   `json:"name"`              // POST body key + DOM id
	Label   string   `json:"label"`             // visible label
	Type    string   `json:"type"`              // "number" | "select" | "text"
	Options []string `json:"options,omitempty"` // for type=select — value == label
	// OptionPairs is the {value,label} alternative to Options. When set,
	// each <option> uses Value as the form value and Label as the
	// visible text. Useful when the id you want sent to the server
	// isn't readable (UUIDs, opaque keys). If both are set,
	// OptionPairs takes precedence.
	OptionPairs []SelectOption `json:"option_pairs,omitempty"`
	Default     string         `json:"default,omitempty"`
	Min         int            `json:"min,omitempty"` // for type=number
	Max         int            `json:"max,omitempty"` // for type=number
}

// ChatMode describes a toggle pill above the chat input. Bound to a
// single boolean field on the GET endpoint. The runtime mixes
// {field: true|false} into the SendURL body so the server sees the
// active modes (e.g. private_mode).
type ChatMode struct {
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"` // tooltip
	GetURL  string `json:"get_url"`
	PostURL string `json:"post_url"`
	Field   string `json:"field"`           // bool field name on GET / POST body
	// SendField is the body field name when sending the chat message
	// — defaults to Field. Use when the server's send-handler key
	// differs from the settings-endpoint key.
	SendField string `json:"send_field,omitempty"`
}

// PipelinePanel renders a "submit form → run pipeline → see structured
// transcript" layout. Designed for debate, but reusable for research,
// autoblog, or any app with a multi-stage streaming run.
//
// Layout:
//   - Left: sidebar of past runs (same chrome as ChatPanel: New +
//     Select, BulkSelect deletes; mobile drawer pattern).
//   - Right: submit form on top (when no active run), live transcript
//     below. Transcript is a vertical stream of TranscriptBlocks —
//     each one a typed, optionally-collapsible card with a markdown
//     body that can stream chunk-by-chunk.
//
// SSE event protocol (POST SubmitURL → SSE):
//   - event: session  data: {id}                  — set session id
//   - event: block    data: {id, type, title}     — start a new block
//   - event: chunk    data: {id, text}            — append to block body
//   - event: block_done data: {id}                — mark block complete
//   - event: status   data: {text}                — soft status line
//   - event: done     data: {}                    — pipeline finished
//   - event: error    data: {message}             — fatal error
//
// SessionLoadURL response: {ID, Title, Date, Blocks: [{type, title, body}, ...]}.
type PipelinePanel struct {
	SessionsListURL    string `json:"sessions_list_url"`
	SessionLoadURL     string `json:"session_load_url"`
	SessionDeleteURL   string `json:"session_delete_url"`
	SessionExportURL   string `json:"session_export_url,omitempty"`   // {id} placeholder; opens in new tab
	SessionExportLabel string `json:"session_export_label,omitempty"` // default "PDF"
	SubmitURL          string `json:"submit_url"`
	CancelURL          string `json:"cancel_url,omitempty"`
	SubmitLabel        string `json:"submit_label,omitempty"` // default "Start"
	// ReconnectURL — when set, the panel auto-attaches to a live
	// pipeline run identified by the URL query param ?session={id}
	// on initial load. Streams the same SSE event shape as SubmitURL.
	// {id} is a placeholder substituted at navigation time.
	ReconnectURL string `json:"reconnect_url,omitempty"`

	// Fields rendered in the submit form. Field name "topic" / "subject" /
	// the first textarea acts as the "title" for new sessions if the
	// server emits a session event without a separate title.
	Fields []PipelineField `json:"fields,omitempty"`

	// Prefill — button that fetches a suggestion and either drops it
	// straight into the field named by PrefillTarget ("text" mode) or
	// pops a list of clickable choices ("list" mode). List mode
	// expects the response to be a JSON array; each entry uses
	// PrefillListQuestionField (default "question") for the value
	// inserted into the field, and PrefillListHookField (default
	// "hook") for an optional muted second line shown alongside.
	PrefillURL    string `json:"prefill_url,omitempty"`
	PrefillLabel  string `json:"prefill_label,omitempty"`
	PrefillTarget string `json:"prefill_target,omitempty"` // field name to populate
	PrefillMode   string `json:"prefill_mode,omitempty"`   // "text" (default) | "list"
	PrefillListQuestionField string `json:"prefill_list_question_field,omitempty"`
	PrefillListHookField     string `json:"prefill_list_hook_field,omitempty"`
	// PrefillMethod — HTTP method used to fetch suggestions.
	// Defaults to GET. Set to "POST" for endpoints that take a
	// request body (e.g. research's /api/suggest-topics accepts
	// {direction: ""} optionally).
	PrefillMethod string `json:"prefill_method,omitempty"`
	// PrefillBody — JSON body sent when PrefillMethod is POST.
	// Trusted-string format, marshalled directly into the request
	// body. Empty + POST sends "{}" so handlers that only care
	// about the call (not the params) still work.
	PrefillBody string `json:"prefill_body,omitempty"`

	// Field name overrides for sidebar records.
	SessionIDField    string `json:"session_id_field,omitempty"`    // default "ID"
	SessionTitleField string `json:"session_title_field,omitempty"` // default "Title"
	SessionDateField  string `json:"session_date_field,omitempty"`  // default "Date"
	SessionBlocksField string `json:"session_blocks_field,omitempty"` // default "Blocks"

	// SessionMetaFields list extra fields to surface under each
	// sidebar row's title — verdict snippet, confidence badge,
	// winner pill, etc. The runtime renders each entry styled by
	// its Style ("text" | "badge" | "pill"). Truncated to keep
	// rows compact.
	SessionMetaFields []SessionMetaField `json:"session_meta_fields,omitempty"`

	BulkSelect bool   `json:"bulk_select,omitempty"`
	Markdown   bool   `json:"markdown,omitempty"`
	EmptyText  string `json:"empty_text,omitempty"`

	// DeepLinkParam — query-string key the page checks on initial
	// load to auto-open / reconnect to a session. Defaults to
	// "session"; apps with their own URL convention (debate uses
	// "debate", research uses "research", blogger uses "article")
	// declare it here. The runtime always also accepts the generic
	// "session", "id", and "run" so legacy links keep working.
	DeepLinkParam string `json:"deep_link_param,omitempty"`

	// Actions render as a toolbar above the transcript and only
	// appear when a session is loaded (live or saved). Each action
	// is a labeled button bound to a URL that fires on click.
	// Use cases: Generate Report, Export PDF, Copy Link, Push to
	// downstream apps. Server endpoints can be GET (open new tab) or
	// POST (fire-and-toast). {id} in the URL is substituted with the
	// active session id.
	Actions []PipelineAction `json:"actions,omitempty"`
}

// SessionMetaField describes one extra piece of summary data
// rendered under a sidebar row. Keep them short — the rail is
// narrow on desktop and even narrower as a mobile drawer.
type SessionMetaField struct {
	Field string `json:"field"`           // JSON key on the summary object
	Label string `json:"label,omitempty"` // optional prefix label
	// Style: "text" (subtitle line), "badge" (small pill), "pill"
	// (colored pill, color picked by Variants map below). Default "text".
	Style string `json:"style,omitempty"`
	// Variants colors a pill differently per value (e.g. WinningSide
	// "for"→green, "against"→red). Keys are the field's value (lower-
	// cased), values are CSS color hex strings.
	Variants map[string]string `json:"variants,omitempty"`
	// Truncate caps the rendered length (0 = no cap).
	Truncate int `json:"truncate,omitempty"`
}

// PipelineAction is one button in the per-session toolbar.
type PipelineAction struct {
	Label string `json:"label"`
	Title string `json:"title,omitempty"` // tooltip
	URL   string `json:"url"`             // {id} substituted at click time
	// ShowIfField names a boolean / non-zero field on the session
	// summary record. When set, the button only renders for sessions
	// whose record has that field truthy. Use for actions that don't
	// apply to every session (e.g. "Descendants" only when the
	// session has at least one child research). Empty = always show.
	ShowIfField string `json:"show_if_field,omitempty"`
	// HideIfField is the inverse — render only when that field is
	// FALSY. Pair with ShowIfField on a sibling action (e.g. one
	// "Consolidate" plain + one "Consolidate ●" highlighted) to
	// switch between two variants based on a per-session flag.
	HideIfField string `json:"hide_if_field,omitempty"`
	// Method:
	//   "open"   (default for GET-style URLs)  — open in new tab
	//   "copy"   — copy the substituted URL to clipboard, show toast
	//   "post"   — POST {} to URL, refresh sidebar on success
	//   "stream" — POST {}, stream SSE response into the transcript
	//              (replaces current view; same protocol as SubmitURL)
	//   "modal"  — open a modal and stream SSE response into it;
	//              modal footer hosts ModalActions for follow-ups
	//              (Save as PDF, Regenerate, etc.)
	Method string `json:"method,omitempty"`
	// Variant: "primary" | "secondary" | "danger". Default secondary.
	Variant string `json:"variant,omitempty"`
	// Confirm — when set, prompt with this text before firing.
	Confirm string `json:"confirm,omitempty"`
	// ModalActions — extra buttons rendered in the modal footer
	// (only for Method="modal"). Each one is a self-contained
	// PipelineAction; Method may be "open", "copy", or a special
	// "regenerate" that re-runs the parent stream with ?regenerate=1.
	ModalActions []PipelineAction `json:"modal_actions,omitempty"`
}

// PipelineField defines one input in the submit form.
type PipelineField struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type"`         // "text" | "textarea" | "number" | "select"
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         int      `json:"max,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Rows        int      `json:"rows,omitempty"` // for textarea
}

func (PipelinePanel) componentType() string { return "pipeline_panel" }
func (c PipelinePanel) MarshalJSON() ([]byte, error) {
	type alias PipelinePanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"pipeline_panel", alias(c)})
}

// AgentLoopPanel models a multi-turn agent workflow with
// operator-in-the-loop confirmation. It's distinct from ChatPanel
// (single conversation thread) and PipelinePanel (one-shot
// structured run): the agent loops with the user, may call tools,
// may pause to ask permission, and may stream rich activity
// alongside the conversation.
//
// Visual shape (left rail + right pane both optional):
//
//	┌──────────┬──────────────────────┬────────────────┐
//	│ Context  │ Conversation         │ Activity       │
//	│ list     │ (user msgs +         │ (tool calls,   │
//	│ (opt.)   │  assistant replies)  │  outputs)      │
//	│          │                      ├────────────────┤
//	│          │                      │ Terminal       │
//	│          │ ┌──────────────────┐ │ (opt., xterm)  │
//	│          │ │ input + send     │ │                │
//	│          │ └──────────────────┘ │                │
//	└──────────┴──────────────────────┴────────────────┘
//
// The conversation/activity boundary is horizontally resizable.
// When Terminal is set, the right pane splits vertically with a
// resizable boundary between activity and terminal.
//
// Left rail — when ListURL/LoadURL/DeleteURL are all unset, the
// rail is hidden and the panel is single-pane on the left. When
// set, the rail lists context records (sessions, workspaces,
// conversations — whatever the app calls them). Apps that need
// the rail filtered by an outer selector (e.g. a parent-record
// picker) pass query params through the URL templates and refresh
// the panel when the outer selection changes.
//
// SSE protocol — every payload is `data: <json>` with a `kind`:
//
//	{kind: "session", id}                    — sets the active context id
//	{kind: "message", role, id, text}        — append a message bubble
//	{kind: "chunk", id, text}                — append text to message id
//	{kind: "chunk_replace", id, text}        — replace message body
//	{kind: "message_done", id}               — finalize (markdown pass)
//	{kind: "activity", type, id?, text}      — append activity row
//	{kind: "activity_update", id, text}      — replace activity row body
//	{kind: "confirm", id, prompt, detail?,
//	  actions: [{label, value, variant?}]}    — operator confirmation
//	{kind: "block", type, id, ...}           — app-registered renderer
//	  (the runtime calls window.UIBlockRenderers[type] with the data)
//	{kind: "block_done", id}                 — finalize an app block
//	{kind: "block_remove", id}               — drop a block
//	{kind: "status", text}                   — status pill (top of pane)
//	{kind: "done"}                           — round complete
//	{kind: "error", text}                    — fatal error
//
// Message roles supported out of the box: "user", "assistant",
// "system". Apps wanting other narration shapes (intent callouts,
// plan checklists, etc.) emit `kind: "block"` with an app-registered
// renderer, keeping the role set generic.
//
// Activity types out of the box: "status" (info line), "cmd"
// (monospace command call-out), "output" (collapsible result),
// "watch" (spinner + label), "error". Apps emitting custom
// activity rows route through `kind: "block"` similarly.
//
// Operator confirmation: when the server emits a confirm event,
// the runtime renders a card in the activity pane with the
// supplied prompt and a button per action. Clicking a button
// POSTs to ConfirmURL with `{id, value}` and the runtime clears
// the card.
type AgentLoopPanel struct {
	// Left rail — drives a generic list of named records. There
	// are two flavors, picked by ListIsContext:
	//
	//   - SESSION mode (default, ListIsContext=false): rows are
	//     past conversations. Clicking one replays its messages
	//     into the conversation pane and binds future sends to
	//     that session id. Best for chat-style apps where each
	//     conversation has its own thread.
	//
	//   - CONTEXT mode (ListIsContext=true): rows are reference
	//     contexts (workspaces, projects, system profiles). The
	//     conversation pane is ephemeral; each send creates a
	//     new chat session on the server. Clicking a row marks
	//     it as the active context — the id rides on every send
	//     body under ListBodyField — but does not clear or
	//     replay the conversation.
	ListURL    string `json:"list_url,omitempty"`    // GET → []record
	LoadURL    string `json:"load_url,omitempty"`    // GET {id} → record (+messages in SESSION mode)
	DeleteURL  string `json:"delete_url,omitempty"`  // DELETE {id}
	// RenameURL — optional. When set, each rail row gets a ✎ button.
	// Clicking prompts for a new name; the runtime POSTs {id, name}
	// to RenameURL, then refreshes the list. Useful for workspaces /
	// projects where the title is user-editable.
	RenameURL string `json:"rename_url,omitempty"`
	NewLabel  string `json:"new_label,omitempty"`  // default "New"
	ListTitle string `json:"list_title,omitempty"` // sidebar header (default "Sessions")
	// Record field overrides (defaults match capitalized
	// chat-style schemas: ID / Title / LastAt / Messages).
	IDField       string `json:"id_field,omitempty"`
	TitleField    string `json:"title_field,omitempty"`
	DateField     string `json:"date_field,omitempty"`
	MessagesField string `json:"messages_field,omitempty"`
	// ListIsContext switches the rail to CONTEXT mode (see above).
	ListIsContext bool `json:"list_is_context,omitempty"`
	// ListBodyField names the JSON key under which the active rail
	// id ships on every send body when ListIsContext is set.
	// Default "context_id". Apps that already key their server on
	// a different name (e.g. "workspace_id") override here.
	ListBodyField string `json:"list_body_field,omitempty"`

	SendURL   string `json:"send_url"` // POST → SSE
	CancelURL string `json:"cancel_url,omitempty"`
	// InjectURL — when set, pressing Send while a session is
	// already in flight POSTs {id, text} here instead of starting
	// a new session. Mirrors legacy servitor's interjection flow:
	// the user can type a follow-up note while the agent is
	// running, and the agent picks it up between rounds.
	InjectURL string `json:"inject_url,omitempty"`
	// ConfirmURL receives POSTs from operator-confirmation card
	// clicks. Body shape: {id, value} where value is the chosen
	// action's value field. Required when the server emits
	// `kind: "confirm"` events.
	ConfirmURL string `json:"confirm_url,omitempty"`
	// EventsURL — optional SSE reconnect endpoint. When set, the
	// runtime can reattach to an in-flight session after a page
	// reload (deep-link or refresh). Server returns the same
	// event stream the original send is producing.
	EventsURL string `json:"events_url,omitempty"`
	// DeepLinkParam — when set, the runtime mirrors the active
	// context id into the URL query string (e.g. ?session=abc).
	// Reloading the page picks up the parameter and reconnects.
	DeepLinkParam string `json:"deep_link_param,omitempty"`
	// Terminal — when set, splits the right pane vertically with
	// activity on top and a terminal below. The terminal field
	// names the WebSocket endpoint the runtime opens to drive
	// xterm.js. Actual xterm wiring is gated by Phase 2b; for
	// now the renderer reserves the pane slot and ships a
	// placeholder.
	Terminal *AgentTerminal `json:"terminal,omitempty"`
	// Empty-state copy for the conversation pane.
	EmptyText string `json:"empty_text,omitempty"`
	// SubmitLabel — input button label, default "Send".
	SubmitLabel string `json:"submit_label,omitempty"`
	// Placeholder for the input textarea.
	Placeholder string `json:"placeholder,omitempty"`
	// Markdown enables a markdown pass on assistant messages once
	// their `message_done` event arrives. Streaming chunks stay
	// plain until done.
	Markdown bool `json:"markdown,omitempty"`
	// BulkSelect adds checkboxes + bulk delete to the sessions
	// sidebar (same shape as ChatPanel).
	BulkSelect bool `json:"bulk_select,omitempty"`
	// Attachments enables a 📎 button on the input row. When set,
	// the runtime base64-encodes selected images and ships them in
	// the send body as `images: [...]`.
	Attachments bool `json:"attachments,omitempty"`
	// Actions — toolbar buttons rendered above the input row.
	// Same Method semantics as ToolbarAction elsewhere
	// (client / post / open / redirect). "client" handlers
	// receive {sessionId, button, action} via
	// window.uiRegisterClientAction — use for app-specific
	// behavior (open a settings modal, switch context, etc.).
	Actions []ToolbarAction `json:"actions,omitempty"`
	// ExtraFields render in a strip beside the input. Each
	// field's current value rides along on every send body so
	// the server sees them as round parameters.
	ExtraFields []ChatField `json:"extra_fields,omitempty"`
	// HideActivity collapses the right-hand activity pane on
	// load. The user can drag the divider to reveal it. Useful
	// for apps whose default flow doesn't surface much activity
	// (only enable when needed).
	HideActivity bool `json:"hide_activity,omitempty"`
}

// AgentTerminal configures the optional bottom-right terminal pane
// of an AgentLoopPanel. The runtime opens a WebSocket to URL and
// pipes bytes to xterm.js (Phase 2b will wire xterm proper; for
// now the pane reserves space and shows a placeholder).
type AgentTerminal struct {
	URL   string `json:"url"`             // WebSocket endpoint
	Title string `json:"title,omitempty"` // pane header (default "Terminal")
}

func (AgentLoopPanel) componentType() string { return "agent_loop_panel" }
func (c AgentLoopPanel) MarshalJSON() ([]byte, error) {
	type alias AgentLoopPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"agent_loop_panel", alias(c)})
}

func (ChatPanel) componentType() string { return "chat_panel" }
func (c ChatPanel) MarshalJSON() ([]byte, error) {
	type alias ChatPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"chat_panel", alias(c)})
}

// ArticleEditor renders a two-pane editor: list of saved articles on
// the left, edit pane on the right. The edit pane has a title input,
// body textarea, save/delete actions, and an inline chat-assistant
// for discussing or rewriting the current article.
//
// Save model: explicit (Save button) — auto-save can be added later.
// Chat assistant: POST to ChatURL with {subject, body, message, mode,
// history}. Response is {type: "chat"|"article", content}. When type
// is "article", the editor's body is replaced with content (after a
// confirmation dialog showing diff stats).
type ArticleEditor struct {
	ListURL   string `json:"list_url"`
	LoadURL   string `json:"load_url"`   // template with {id}
	SaveURL   string `json:"save_url"`
	DeleteURL string `json:"delete_url"` // template with {id}
	ChatURL   string `json:"chat_url"`

	// Field name mapping — defaults match the schema used by the
	// existing techwriter ArticleRecord. Override only when wiring a
	// different storage shape.
	IDField      string `json:"id_field,omitempty"`      // default "ID"
	SubjectField string `json:"subject_field,omitempty"` // default "Subject"
	BodyField    string `json:"body_field,omitempty"`    // default "Body"
	DateField    string `json:"date_field,omitempty"`    // default "Date"

	// Empty-state copy.
	EmptyText string `json:"empty_text,omitempty"`
	// PlaceholderTitle / PlaceholderBody set the input placeholders.
	PlaceholderTitle string `json:"placeholder_title,omitempty"`
	PlaceholderBody  string `json:"placeholder_body,omitempty"`
	// BulkSelect adds checkboxes to the sidebar items and a bulk-action
	// bar at the top of the list. Supports bulk delete via DeleteURL
	// repeated per selection. Off by default — apps opt in.
	BulkSelect bool `json:"bulk_select,omitempty"`

	// Optional toolbar features. Leave any URL blank to hide that
	// control. All endpoints are app-specific so the framework stays
	// generic. Templates with {id} get the current article's id
	// substituted before fetch.
	// Rules + Merge — still managed by the framework because their
	// slide-in panels live in core/ui/runtime.go. Will move out
	// alongside a generic SlidePanel primitive.
	RulesURL         string `json:"rules_url,omitempty"`          // GET → {rules}; POST {rules}
	MergeURL         string `json:"merge_url,omitempty"`          // POST {subject, body, other, mode, guidance} → {type, content}
	MergeSourcesURL  string `json:"merge_sources_url,omitempty"`  // GET → []source; POST → source
	MergeSourceURL   string `json:"merge_source_url,omitempty"`   // GET/DELETE {id} → source
	RevisionsListURL string `json:"revisions_list_url,omitempty"` // GET {id} → array of {id, date}
	RevisionLoadURL  string `json:"revision_load_url,omitempty"`  // GET {revid} → revision record
	// ImageField is the JSON field name on the article record that
	// holds the header image URL. Default "ImageURL". Set blank to
	// disable image persistence (the editor still surfaces images
	// supplied by client actions but won't round-trip them).
	ImageField string `json:"image_field,omitempty"`

	// ExtraActions populates a "More ▾" popover at the right end of
	// the toolbar with app-defined actions. See MenuAction for the
	// shape.
	ExtraActions []MenuAction `json:"extra_actions,omitempty"`

	// Actions is the declarative toolbar — list of buttons rendered
	// between the title input and the "More ▾" popover. Each entry
	// dispatches by Method: "client" runs an app-registered callback,
	// "post" POSTs to URL, "open" / "redirect" navigate. "builtin"
	// remains for the framework-managed rules / merge slide-in panels
	// only; app-specific flows should use "client".
	Actions []ToolbarAction `json:"actions,omitempty"`
}

// MenuAction is one entry in an ExtraActions / popover-style
// declarative menu. Generic enough to drop into any app's toolbar:
// the runtime renders the menu, the action wiring lives on the
// caller side via URL + method.
type MenuAction struct {
	Label  string `json:"label"`
	Title  string `json:"title,omitempty"`  // tooltip / accessibility text
	URL    string `json:"url,omitempty"`    // {id} substituted at click time
	Method string `json:"method,omitempty"` // "post" | "open" | "redirect" | "builtin"
	// Confirm shows a confirm() dialog before firing. Empty = no prompt.
	Confirm string `json:"confirm,omitempty"`
}

// ToolbarAction is one entry in a declarative toolbar (e.g.
// ArticleEditor.Actions). Rendered as a button in the order declared.
// Method semantics match MenuAction's, plus an optional Variant for
// styling ("primary" = accent, "danger" = red, "" = default).
//
// When Method == "client", URL names a callback the app registered
// via window.uiRegisterClientAction. The callback receives an
// editor handle (read/write body/title/image plus save/toast/busy
// helpers) so all app-specific behavior lives in the app's package.
//
// When Method == "builtin", URL names a flow the renderer manages
// directly. Only "rules" and "merge" still use this path — they
// own slide-in panels that haven't been factored into a generic
// SlidePanel primitive yet.
type ToolbarAction struct {
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	Method  string `json:"method,omitempty"`
	Confirm string `json:"confirm,omitempty"`
	Variant string `json:"variant,omitempty"` // "primary" | "danger" | "" (default)
}

func (ArticleEditor) componentType() string { return "article_editor" }
func (a ArticleEditor) MarshalJSON() ([]byte, error) {
	type alias ArticleEditor
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"article_editor", alias(a)})
}

// CodeWriterPanel is the codewriter app's specialized two-pane layout —
// a snippet sidebar + a code editor (with optional Context block) on
// the left, and a dual-mode chat ("Chat" discusses, "Edit" proposes
// fenced code that's applied via inline diff) on the right. Supports
// {{NAME}} variable substitution, a saved-values library, and a saved-
// contexts library. Distinct enough from ArticleEditor / ChatPanel
// that it gets its own component type rather than overloading either.
type CodeWriterPanel struct {
	// Snippet CRUD endpoints.
	ListURL   string `json:"list_url"`             // GET → array of snippets
	LoadURL   string `json:"load_url,omitempty"`   // GET {id} → snippet (defaults to "{list_url}/{id}" pattern when blank)
	SaveURL   string `json:"save_url"`             // POST snippet
	DeleteURL string `json:"delete_url,omitempty"` // DELETE {id}

	// Chat endpoint. POST {name, lang, code, context, message, mode, history}
	// → {response, code?} — code present only on mode="edit" successes.
	ChatURL string `json:"chat_url"`

	// Optional toolbar / library endpoints. Leave blank to hide.
	SuggestNameURL   string `json:"suggest_name_url,omitempty"`
	RevisionsListURL string `json:"revisions_list_url,omitempty"` // GET {id}
	RevisionLoadURL  string `json:"revision_load_url,omitempty"`  // GET {revid}
	MarkLatestURL    string `json:"mark_latest_url,omitempty"`    // POST {revid}
	ValuesListURL    string `json:"values_list_url,omitempty"`
	ValueURL         string `json:"value_url,omitempty"` // GET/PUT/DELETE {id}
	ContextsListURL  string `json:"contexts_list_url,omitempty"`
	ContextURL       string `json:"context_url,omitempty"` // GET/PUT/DELETE {id}

	// Field name mapping — defaults match SnippetRecord.
	IDField   string `json:"id_field,omitempty"`   // default "id"
	NameField string `json:"name_field,omitempty"` // default "name"
	LangField string `json:"lang_field,omitempty"` // default "lang"
	CodeField string `json:"code_field,omitempty"` // default "code"
	VarsField string `json:"vars_field,omitempty"` // default "vars"
	DateField string `json:"date_field,omitempty"` // default "date"

	// Languages populates the lang dropdown. Leave nil to use defaults
	// (bash, sql, python, powershell, go, regex, other).
	Languages []string `json:"languages,omitempty"`

	// Empty-state copy + placeholder text.
	EmptyText        string `json:"empty_text,omitempty"`
	PlaceholderName  string `json:"placeholder_name,omitempty"`
	PlaceholderCode  string `json:"placeholder_code,omitempty"`
	PlaceholderCtx   string `json:"placeholder_ctx,omitempty"`
	PlaceholderChat  string `json:"placeholder_chat,omitempty"`
}

func (CodeWriterPanel) componentType() string { return "codewriter_panel" }
func (c CodeWriterPanel) MarshalJSON() ([]byte, error) {
	type alias CodeWriterPanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"codewriter_panel", alias(c)})
}

// Card is a free-form container that just renders raw HTML. Use
// sparingly — escape hatch for things the framework doesn't model yet.
type Card struct {
	HTML string `json:"html"`
}

func (Card) componentType() string { return "card" }
func (c Card) MarshalJSON() ([]byte, error) {
	type alias Card
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"card", alias(c)})
}
