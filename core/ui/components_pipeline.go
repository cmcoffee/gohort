package ui

import (
	"encoding/json"
)

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

	// Prefill — button that fetches a suggestion for the field named by
	// PrefillTarget. The RESPONSE decides how it lands, so there is no mode to
	// declare: a JSON array pops a list of clickable choices, an object or a
	// bare string drops straight into the field. Each list entry uses
	// PrefillListQuestionField (default: topic / text / question) for the value
	// inserted, and PrefillListHookField (default: hook / description /
	// summary) for an optional muted second line.
	PrefillURL               string `json:"prefill_url,omitempty"`
	PrefillLabel             string `json:"prefill_label,omitempty"`
	PrefillTarget            string `json:"prefill_target,omitempty"` // field name to populate
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
	SessionIDField     string `json:"session_id_field,omitempty"`     // default "ID"
	SessionTitleField  string `json:"session_title_field,omitempty"`  // default "Title"
	SessionDateField   string `json:"session_date_field,omitempty"`   // default "Date"
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
	Type        string   `json:"type"` // "text" | "textarea" | "number" | "select" | "toggle" | "file"
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         int      `json:"max,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Rows        int      `json:"rows,omitempty"` // for textarea
	// File-field wiring (Type "file"). The picked file is multipart-
	// POSTed to UploadURL, which extracts its text server-side and
	// returns JSON {"text": "...", "title": "..."}; the runtime drops
	// text into the field named by UploadTarget so the user reviews it
	// before submitting, and title into a "title" field when present.
	// Accept is the input's accept attribute (e.g. ".pdf,.docx,.txt").
	// The file field itself is never part of the submit body — it only
	// populates other fields. Generic: any pipeline app can attach a
	// server-side extractor endpoint this way.
	Accept       string `json:"accept,omitempty"`
	UploadURL    string `json:"upload_url,omitempty"`
	UploadTarget string `json:"upload_target,omitempty"`
	// UploadSetField / UploadSetValue — on a successful upload, set the
	// sibling field named by UploadSetField to the constant UploadSetValue
	// (e.g. flip a "source" select to "upload"). Left untouched when
	// UploadSetField is empty.
	UploadSetField string `json:"upload_set_field,omitempty"`
	UploadSetValue string `json:"upload_set_value,omitempty"`
}

func (PipelinePanel) componentType() string { return "pipeline_panel" }

func (c PipelinePanel) MarshalJSON() ([]byte, error) {
	type alias PipelinePanel
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"pipeline_panel", alias(c)})
}
