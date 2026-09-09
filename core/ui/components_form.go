package ui

import (
	"encoding/json"
)

// FormPanel renders a list of labeled input fields bound to a single
// JSON record (Source URL). Each field's change saves the full record
// back to Source. Text/textarea fields are debounced so we don't POST
// on every keystroke.
//
// Method defaults to POST. Use "PATCH" + only-the-changed-field saving
// for endpoints that don't accept full-record overwrites.
type FormPanel struct {
	Source string `json:"source"`
	Method string `json:"method,omitempty"`
	// SideNav renders a FormPanel's "header" fields as a left navigation rail
	// instead of collapsed accordions: every group is listed at once and the
	// selected one is shown. Same grouping rule (a header owns the fields
	// until the next header), so a form opts in without touching its field
	// list; fields before the first header stay pinned above the rail as the
	// form's identity.
	//
	// For a long settings form, accordions hide how much there is — every
	// section reads as shut and finding one setting means opening each in
	// turn. Ignored when the form has no headers, or in Steps mode.
	SideNav bool `json:"side_nav,omitempty"`

	Fields []FormField `json:"fields"`
	// Steps — when set, the form renders as a multi-step wizard
	// instead of one flat field list: a numbered progress rail, one
	// step's fields visible at a time, Back/Next navigation, and the
	// submit button (plus Test/Reset, if configured) on the final
	// step. Fields is ignored when Steps is set — every field lives
	// inside a step. All steps still bind to the ONE record `current`
	// state, so the final submit POSTs exactly what a flat form
	// would; the wizard is purely presentation. Next is gated on the
	// step's Required fields, and a step's ShowWhen can skip the
	// whole step based on an earlier answer. Use for guided create
	// flows ("New Agent", connector onboarding); keep flat Fields
	// for edit-in-place settings forms.
	Steps []FormStep `json:"steps,omitempty"`
	// PostURL — destination for saves when it differs from Source.
	// Defaults to Source. Use when the GET endpoint that returns the
	// current record shape isn't the right write target — e.g. an
	// edit form whose Source is `api/record/{id}` (returns the row)
	// but whose saves go to `api/records` (the list endpoint that
	// handles both create and update via the ID field on the body).
	PostURL string `json:"post_url,omitempty"`

	// SubmitLabel — when set, the form switches from per-field
	// debounced auto-save to explicit submit-button mode. Field
	// changes update local state only; the POST fires when the user
	// clicks the submit button. Use for "fill in this form and
	// create something" flows; leave empty for the in-place edit
	// pattern (every blur saves).
	SubmitLabel string `json:"submit_label,omitempty"`
	// RedirectURL — after a successful submit-button POST, navigate
	// to this URL. `{field}` placeholders substitute from the
	// response JSON body (`{id}`, `{session}`, etc.), so a create-form
	// can redirect to the freshly-allocated record's page.
	RedirectURL string `json:"redirect_url,omitempty"`
	// RedirectTarget — "_blank" opens in a new tab (default),
	// "_self" replaces the current page. Only used when RedirectURL
	// is set.
	RedirectTarget string `json:"redirect_target,omitempty"`

	// TestURL — when set, renders a "Test" button alongside the form.
	// Click POSTs the form's CURRENT (possibly-unsaved) values to this
	// URL; the server is expected to respond with JSON
	//   {"ok": true,  "message": "..."}  → green check + message
	//   {"ok": false, "error":   "..."}  → red x + error
	// or any non-2xx → red x + status text. Result renders inline next
	// to the button. Use for connectivity / credential checks (SMTP,
	// embedding endpoint, search API, image-gen API) so operators can
	// validate before saving.
	TestURL string `json:"test_url,omitempty"`
	// TestLabel — button text for the Test affordance. Defaults to
	// "Test connectivity" when TestURL is set and this is empty.
	TestLabel string `json:"test_label,omitempty"`

	// ResetURL — when set, renders a "Revert to defaults" button. Click
	// confirms, POSTs to this URL (the server clears the stored overrides so
	// the fields fall back to their code/config defaults), then re-loads the
	// form from Source to show the reverted values. Domain-agnostic: the app
	// supplies the URL and decides what "default" means server-side.
	ResetURL string `json:"reset_url,omitempty"`
	// ResetLabel — button text for the reset affordance. Defaults to
	// "Revert to defaults" when ResetURL is set and this is empty.
	ResetLabel string `json:"reset_label,omitempty"`
	// ResetConfirm — confirmation prompt before the reset POST. Defaults to a
	// generic warning when ResetURL is set and this is empty.
	ResetConfirm string `json:"reset_confirm,omitempty"`

	// Templates — optional named presets. When set, the form renders a
	// "Start from template" dropdown above the fields; picking one
	// applies its Values to the matching fields (via the same per-field
	// setters the Suggest button uses), giving create-forms a
	// known-good starting point the user can edit before saving. Keys
	// in each template's Values are field names.
	Templates []FormTemplate `json:"templates,omitempty"`
	// TemplatesLabel overrides the "Start from template" caption on the presets
	// dropdown — e.g. "Agent type" when the templates are character presets, not
	// just starting points. Empty = "Start from template".
	TemplatesLabel string `json:"templates_label,omitempty"`

	// Invalidate — data sources to refresh after a successful save. Each
	// entry is matched against other components' Source; a Table fetched
	// from the same URL refetches itself (via window.uiInvalidate). Use
	// when this form writes a record that a sibling list displays — e.g.
	// an "add" form in a modal whose result should appear in the table
	// behind it without a manual reload.
	Invalidate []string `json:"invalidate,omitempty"`
	// OnSuccess names a client action (registered with
	// window.uiRegisterClientAction) called after a successful submit,
	// with {response, form, ctx}. The response is the server's decoded
	// JSON body.
	//
	// For the case where the SERVER produces something the form could not
	// have known and the operator has to act on once — a one-time secret,
	// a generated link, an id to hand to somebody. A toast is the wrong
	// surface for those: it holds one line and takes it away again.
	// Deliberately a handler name rather than any built-in rendering,
	// because what to do with the response is the app's business and
	// core/ui has no way to be right about it.
	OnSuccess string `json:"on_success,omitempty"`
}

// FormTemplate is one named preset for a FormPanel's "Start from
// template" dropdown. Values maps field names to prefill values.
type FormTemplate struct {
	Label  string         `json:"label"`
	Values map[string]any `json:"values"`
}

// FormStep is one page of a FormPanel wizard (FormPanel.Steps).
// Fields render in order within the step and support every FormField
// type, including "header" groups and "hidden" context carriers.
type FormStep struct {
	// Title labels the step in the progress rail ("Purpose",
	// "Access", "Review"). Keep it to one or two words.
	Title string `json:"title"`
	// Intro — optional lead-in paragraph rendered above the step's
	// fields. Use it to tell the user what this step decides and why,
	// since a wizard's whole point is guidance.
	Intro string `json:"intro,omitempty"`
	// ShowWhen gates the entire step on the form's current values,
	// using the same expression grammar as FormField.ShowWhen
	// ("field", "!field", "field:value", "field:v1|v2", clauses joined
	// by ";"). A hidden step is skipped by Back/Next, dropped from the
	// rail, and exempt from Required gating — e.g. show an extra step
	// only when an earlier answer picked a matching kind, or hide the
	// guided steps once a "template" answer short-circuits them.
	ShowWhen string      `json:"show_when,omitempty"`
	Fields   []FormField `json:"fields"`
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
//   - "select"   — single-choice dropdown with Options
//   - "checklist" — multi-select vertical checkbox list with Options;
//     saves as a JSON string array of checked Values.
//     Each option may carry a Help line that renders as
//     a small subtitle under its label. For "allowlist
//     N items from this fixed set" configs.
//   - "tel"      — phone number (better mobile keyboard)
//   - "rules"    — line-separated list editor (one input per row,
//     saves as a newline-joined string)
//   - "tags"     — compact chip array editor (saves as a JSON
//     string array), suited for keyword-style fields
//   - "sections" — structured markdown editor. Reads as the finished
//     document (rendered markdown, scrollable, capped at
//     Rows) with an "✎ Edit" button; editing presents the
//     value as an outline of headed blocks, each edited as
//     prose or as a +/- list, with a Raw view alongside.
//     Set Inline to keep the editor permanently open.
//     Declare the outline with Sections; see SectionSpec
//     for the round-trip contract.
//   - "toggle"   — iOS-style switch bound to a boolean field
//   - "password" — masked input (renders as <input type="password">).
//     Pair with a server convention where GET returns
//     the placeholder "(configured)" for an existing
//     secret and POST only updates when the field
//     differs from that placeholder; otherwise the user
//     re-saving the form would overwrite the stored
//     secret with the placeholder.
//   - "header"   — visual section divider; no input, no value binding.
//     Renders Label as a section title and Help as an
//     optional subtitle below it. Use to split a long
//     FormPanel into grouped chunks (Identity / Persona /
//     Memory / Privacy / etc.) without breaking the
//     single-save pattern. Field name is ignored.
//   - "readonly" — displays the value from Source and contributes nothing
//     to the save payload. For a form that must SHOW something
//     computed — a derived status, a summary of what the
//     current settings amount to — alongside the inputs that
//     change it, without pretending it is editable. Line
//     breaks in the value are preserved.
//   - "hidden"   — contributes Default to the save payload but renders
//     nothing. Use for context-derived values the page
//     knows up front (e.g. "owned_by = <parent_id>" on a
//     new sub-agent form). Default is seeded into the
//     form state immediately so the first save POSTs it.
type FormField struct {
	// Columns declares the sub-fields of a "rows" field — the repeating
	// list editor. Each column is itself a FormField, so a row cell is
	// text / number / select / toggle / textarea / combo with the same
	// vocabulary the rest of a form uses.
	//
	// "combo" is a text cell with a wheel: the caller supplies Options
	// as suggestions (a native <datalist>), and the person either picks
	// one or types their own. Use it where a value is usually one of a
	// known set but the set is not a fence — a name that may be one of
	// the framework's built-ins, or anything the author invents.
	//
	// The value of a "rows" field is a plain array of objects keyed by
	// the columns' Field names, so an endpoint keeps its natural shape
	// (`output: [{name, type, required}]`) instead of round-tripping
	// through a JSON textarea — which is what every structured editor
	// here did before this existed, because the toolkit had no repeating
	// shape at all.
	//
	// Order is preserved and editable (↑↓ per row), because in most of
	// these the order IS meaning: the sequence fields are declared in,
	// the order steps run.
	Columns []FormField `json:"columns,omitempty"`
	// ReloadOnChange reloads the page after this field's save succeeds.
	// For fields that are part of the record's ADDRESS: renaming the
	// thing the form posts to leaves every sibling control on the page —
	// including this form's own post URL — pointing at a name that no
	// longer exists, and a reload is the honest way to catch them all
	// up. Auto-save fields only; a submit-mode form redirects instead.
	//
	// It also makes a text field COMMIT ON BLUR rather than on the usual
	// typing debounce. Both halves are the same decision: a field whose
	// save reloads the page cannot fire 600ms after a pause, or it
	// renames the record to half a word and pulls the page out from
	// under the person still typing the rest.
	ReloadOnChange bool `json:"reload_on_change,omitempty"`

	// A note that applies to "select" and "checklist" alike: Options
	// usually come from a LIVE set, and things leave those sets. A
	// stored value with no option left is rendered anyway, marked "no
	// longer available" — visible in the select, and in the checklist
	// checked, carried through saves, and removed only by unticking it.
	// Nothing an app has to ask for: a control that hides part of its
	// own value cannot be trusted, and one that drops it on the next
	// unrelated save is making an edit nobody asked for.

	// OwnLine puts a "rows" column on its own full-width line beneath
	// the row's other cells, with its Label above it.
	//
	// For the cell that holds an INSTRUCTION rather than an identifier.
	// Sharing a line with the short cells means either it is too narrow
	// to write in or they are too narrow to read; on its own line both
	// are what they need to be, and the row reads as a small record
	// rather than a squeezed strip. A row with one of these is drawn as
	// a bordered block so it still reads as ONE row.
	OwnLine bool `json:"own_line,omitempty"`

	// HideWhen and LockWhen are ROW-SCOPED conditions on a "rows"
	// column, written in the same grammar as ShowWhen ("field:value",
	// "field:a|b", "!field", chained with ";") but evaluated against the
	// ROW rather than the whole record.
	//
	// HideWhen removes the cell; LockWhen renders the value as text with
	// no way to edit it, labelled from the column's own Options when one
	// matches. Together they express "this row has settled into a kind
	// where the rest of these controls do not apply" — which is the
	// alternative to leaving a control somebody can change and be
	// ignored for. A setting that silently does nothing is worse than a
	// setting that is not offered.
	//
	// Changing a cell redraws the row when any column declares one, so
	// the row reflects what it has become rather than what it was.
	HideWhen string `json:"hide_when,omitempty"`
	LockWhen string `json:"lock_when,omitempty"`

	// AddLabel overrides the "+ Add" button's text on a "rows" field.
	// Name the THING being added ("+ Add output"), not the act.
	AddLabel string `json:"add_label,omitempty"`
	// Width is a flex weight for a column inside a "rows" field, so a
	// description can be wider than a type. Ignored elsewhere.
	Width int `json:"width,omitempty"`

	Field       string `json:"field"`
	Label       string `json:"label,omitempty"`
	Type        string `json:"type,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	Rows        int    `json:"rows,omitempty"`
	// Expand / Inline control how a "textarea" field is presented. A large
	// prompt or JSON blob peeked through a short inline box is hard to read
	// and edit, so a textarea can instead render as a read-only clamped
	// preview plus an "Edit" button that opens the whole value in a wide,
	// tall modal editor (the shared uiOpenModal). Default: auto — any
	// textarea with Rows >= 6 gets the preview+Edit treatment; smaller ones
	// stay plain inline boxes. Expand forces it on regardless of Rows;
	// Inline forces the plain inline box regardless of Rows.
	//
	// A "sections" field reads the same way but is preview-first
	// unconditionally (Expand is redundant there); Inline keeps its
	// outline editor permanently open. Other field types ignore both.
	Expand bool `json:"expand,omitempty"`
	Inline bool `json:"inline,omitempty"`
	// SingleLine opts a "text" / "tel" field OUT of the multi-line paste
	// upgrade. By default, pasting text containing a newline into a
	// single-line field swaps it to a growing textarea in place, because an
	// <input> physically cannot hold newlines (the HTML value-sanitization
	// algorithm strips CR/LF) and the paste would otherwise be silently
	// flattened into one line. Set this on a value that must stay one line —
	// a handle, a URL, a slug, an API key — where a flattened paste is
	// preferable to a field that accepts structure the backend will reject.
	// Ignored for field types that are already multi-line.
	SingleLine bool `json:"single_line,omitempty"`
	Min        int  `json:"min,omitempty"`
	Max        int  `json:"max,omitempty"`
	// Decimals enables float input on a "number" field. 0 = integer
	// only (default). >0 = parseFloat with that many decimal places
	// in the saved value (use 4 for per-1K-token rates like 0.0003).
	Decimals int            `json:"decimals,omitempty"`
	Options  []SelectOption `json:"options,omitempty"`
	// Multiple turns a "select" field into a multi-select that saves an ARRAY.
	// For a field that legitimately takes several values — the sources a mode
	// consults, the collections it searches — where the alternative was a
	// second component with its own endpoint contract for what is, in the
	// browser, one attribute on the control that was already there.
	Multiple bool `json:"multiple,omitempty"`
	// Suggestions offers existing values on a text field WITHOUT constraining
	// it to them (rendered as a native datalist). Use wherever the value is
	// free-form but reuse should be the easy path — claiming an existing
	// category name rather than coining a near-duplicate ("Calendar" vs
	// "calendars"). A select would be wrong there: it forbids the new value
	// that the field exists to allow.
	Suggestions []string `json:"suggestions,omitempty"`
	// Collapsed, on a Type=="header" field, makes that header a collapsible
	// group: the fields that follow it (until the next header) fold into a body
	// that's hidden until the header is clicked. Declutters advanced settings
	// without splitting the single-save FormPanel. No effect on non-header fields.
	Collapsed bool `json:"collapsed,omitempty"`
	// ShowWhen gates this field on the form's current values; it is
	// rendered (and saves are wired) only while the expression holds.
	// Grammar: "field" (truthy), "!field" (falsy/empty), "field:value",
	// "field:v1|v2" (membership), "field:!v1|v2" (NOT one of those —
	// the only way to write a condition that holds while the field is
	// still untouched); clauses joined by ";" must ALL match.
	// Use to collapse irrelevant configuration when a master toggle is
	// off — e.g. hide a whisper URL until `enabled` is on. Updates
	// immediately when the gating field changes.
	ShowWhen string `json:"show_when,omitempty"`
	// Required — in a Steps wizard, the user can't advance past (or
	// submit from) this field's step while it is empty (null, "", or
	// an empty array; 0 and false count as filled). The offending
	// field highlights and its label appears in an inline note. No
	// effect on flat (non-Steps) forms today. Note for "select":
	// the browser shows the first option pre-picked but the value
	// only enters the record when the user touches the control — give
	// a required select a blank "— choose —" first option so the
	// display matches the gate.
	Required bool `json:"required,omitempty"`

	// Chips — when ChipsSource is set, the field renders with a row
	// of clickable preset chips above the input. Each chip applies a
	// preset value to the field. Designed for things like persona
	// pickers where users want fast access to saved presets plus
	// optional AI-assisted creation of new ones.
	ChipsSource     string `json:"chips_source,omitempty"`      // GET → [{id, name, <value-field>, builtin?}]
	ChipsValueField string `json:"chips_value_field,omitempty"` // field on each chip whose value goes into the input (default "value")
	// ChipsCreate enables "+ New" affordance. POSTed body =
	// {name, <value-field>: "..."}. Server returns updated list.
	ChipsCreateURL string `json:"chips_create_url,omitempty"`
	// ChipsDeleteURL deletes a custom chip. {id} substituted at click.
	// Only fires for non-builtin chips (double-click to delete).
	ChipsDeleteURL string `json:"chips_delete_url,omitempty"`
	// ChipsAssistURL takes a seed (POST {seed}) and returns plain
	// text — used for the "AI Assist" button inside the create dialog.
	ChipsAssistURL string `json:"chips_assist_url,omitempty"`
	// ChipsAddLabel — text on the "+ New" chip; defaults to "+ New".
	ChipsAddLabel string `json:"chips_add_label,omitempty"`
	// ChipsAlsoSet — additional form fields to populate when a chip
	// is picked. Map keys name target form fields; values name the
	// property on the chip record to read. Example: persona chips
	// carry both {personality, name}; the personality chip-picker
	// declares ChipsAlsoSet: {"persona_name": "name"} so picking a
	// persona auto-fills the separate Persona name field with the
	// persona's name. Generic — any chip with multiple useful
	// fields can fan them out into companion form inputs.
	ChipsAlsoSet map[string]string `json:"chips_also_set,omitempty"`

	// AssistPrompt tells the assistant what THIS field is for, in the
	// app's own words: who it should write as, what the value is used
	// for downstream, what to avoid. Folded into the assist
	// conversation's framing alongside whatever guidance the endpoint
	// already has. Without it the assistant knows only the field's name,
	// label, and help text, which is usually too thin to write well from.
	//
	// It rides to the server through the browser, so treat it as
	// instructions about the writing task and nothing more: no secrets,
	// no authorization logic, nothing whose integrity matters. The
	// endpoint composes it INTO its own framing rather than being
	// replaced by it, so a tampered value can shape the advice but
	// cannot escape the response contract or reach another field.
	AssistPrompt string `json:"assist_prompt,omitempty"`

	// SuggestURL enables a per-field "✨ Suggest" button that asks
	// the server to generate (or refine) this field's value via the
	// app's LLM. Click → optional hint prompt → POST {field, hint,
	// record} → server returns {value} → setter applies based on
	// field type and triggers save. Supported types: text, textarea,
	// number, rules.
	//
	// A server may answer {values: [...]} instead of {value} to offer
	// SEVERAL candidates: the runtime shows them as a pick-one list with
	// "write my own" alongside, and applies the chosen one through the
	// same setter. Naming is what this is for — one generated name is a
	// guess to argue with, three are a choice to make.
	//
	// Long-text types ("textarea", "rules") open the assist workbench
	// instead of the hint prompt: the draft beside a conversation, with
	// a walk back through earlier versions. Those turns POST the same
	// body plus {message, draft, history, assist_prompt} and read
	// {reply, value} — an endpoint that ignores the extra keys and
	// answers {value} still works, it just behaves as one-shot.
	//
	// A "sections" field puts the ✨ on each SECTION rather than on the
	// field, so drafting revises one part of the document and leaves the
	// rest alone. Reaching it means opening the editor: the reading view
	// has no ✨ at all, because regenerating an entire prompt from one
	// click next to a preview is the mistake per-section drafting exists
	// to prevent. Raw view is the exception and gets a whole-document
	// "✨ Draft" — there are no sections there to target, and someone in
	// raw mode has already chosen to work on the document as a whole.
	SuggestURL string `json:"suggest_url,omitempty"`

	// SuggestOnOpen fetches the suggestion as soon as the field's STEP
	// becomes visible, rather than waiting for the button, and skips the
	// hint prompt (nobody is there to answer one on arrival). Only fires
	// when the field is still empty, and only once per field, so stepping
	// back and forward does not re-roll an answer the user has seen.
	//
	// For a field whose whole purpose is to offer choices: a naming step
	// that arrives with names beats a blank box next to a button.
	SuggestOnOpen bool `json:"suggest_on_open,omitempty"`

	// RowEditor adds an "Edit" button to each row of a Type=="rules" list,
	// which swaps the field in place for a single-rule editor: a full-height
	// box for the one line, plus a "describe the change" composer wired to
	// SuggestURL. For a list whose items are one WORD (a controlled value, a
	// scope) this is noise, so it is opt-in; for a list whose items are two
	// sentences of house style, a one-line input shows about a third of what
	// you are editing.
	//
	// In place rather than a nested dialog: the rules field already tends to
	// live inside a modal, and a dialog inside a dialog costs two layers of
	// focus and Escape handling to change one sentence.
	RowEditor bool `json:"row_editor,omitempty"`

	// Presets — small inline static list of one-click fills shown
	// above the input. Click a preset to populate the field with
	// its value (and save / mark dirty in the usual way). Use for
	// "common values for this field" — e.g. canonical endpoint
	// URLs for popular providers, default model names, common
	// timeout values. Static-only; for dynamic lists (model browser,
	// user-curated presets) use ChipsSource instead.
	Presets []FieldPreset `json:"presets,omitempty"`

	// Default seeds the form's local state for this field at render
	// time. Used by Type="hidden" to bake a context-derived value into
	// the save payload (e.g. owned_by=<parent_id> on a new sub-agent
	// form). Visible field types ignore this — they pull their initial
	// value from the loaded record (Source URL); use intake or a
	// SuggestURL when you want a default a user can edit.
	Default string `json:"default,omitempty"`

	// Sections declares the outline a Type=="sections" field offers —
	// the areas the author is expected to fill in. Declared sections the
	// stored value doesn't have yet render as empty, labeled slots, so
	// the structure is visible before it's written; they contribute
	// NOTHING to the saved markdown until they hold content. Headings
	// found in the value that aren't declared render in document order
	// alongside them and keep whatever shape their content implies.
	//
	// The outline SEEDS a document, it does not constrain one. The saved
	// value is ordinary markdown: the author can reorder any section
	// (declared ones included), reshape it, or ignore a slot entirely,
	// and order is carried by the markdown itself. Declare what usually
	// belongs here, not what must. Ignored by other field types.
	Sections []SectionSpec `json:"sections,omitempty"`
	// SectionsAllowFree lets the user add sections beyond the declared
	// outline (and delete the ones they added). Off by default: a field
	// whose skeleton IS the contract — a form the server parses back by
	// heading — should not grow headings the server won't read.
	SectionsAllowFree bool `json:"sections_allow_free,omitempty"`
	// SectionsLevel is the markdown heading level used for declared and
	// newly added sections (default 2, i.e. "## Title"). Existing
	// headings in the value keep the level the author wrote.
	SectionsLevel int `json:"sections_level,omitempty"`

	// Accept sets the file picker's accept filter (e.g. ".json") for a
	// Type=="file" field. That field renders a native file chooser; the
	// picked file is read as text ENTIRELY in the browser (no upload, no
	// endpoint) and its contents become the field's submitted value,
	// with the filename shown as confirmation. Use for "import this
	// file" flows where the file's text IS the value. Ignored by other
	// field types.
	Accept string `json:"accept,omitempty"`
}
