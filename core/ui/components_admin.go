package ui

import (
	"encoding/json"
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
	Source  string   `json:"source"` // GET + POST endpoint
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
	Source            string `json:"source"`
	PostTo            string `json:"post_to"`
	Method            string `json:"method,omitempty"`              // default POST
	Field             string `json:"field,omitempty"`               // default "members"
	HandleField       string `json:"handle_field,omitempty"`        // default "handle"
	NameField         string `json:"name_field,omitempty"`          // default "name"
	AliasesField      string `json:"aliases_field,omitempty"`       // default "aliases"
	AliasHandlesField string `json:"alias_handles_field,omitempty"` // optional sibling field on the parent record (e.g. "alias_handles") — comma-sep textbox
	EmptyText         string `json:"empty_text,omitempty"`
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
	ListURL       string `json:"list_url"`
	CreateURL     string `json:"create_url"`
	DeleteURL     string `json:"delete_url"`
	NameField     string `json:"name_field,omitempty"`      // default "name"
	IDField       string `json:"id_field,omitempty"`        // default "id"
	SecretField   string `json:"secret_field,omitempty"`    // default "key"
	CreatedField  string `json:"created_field,omitempty"`   // default "created"
	LastSeenField string `json:"last_seen_field,omitempty"` // default "last_seen"
	NewLabel      string `json:"new_label,omitempty"`       // default "+ New API key"
	EmptyText     string `json:"empty_text,omitempty"`
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

// HistoryPanel renders a scrollable list of messages fetched from
// Source. Used inside RowAction Expand for chat-history-style displays.
type HistoryPanel struct {
	Source       string `json:"source"`
	Header       string `json:"header,omitempty"`
	EmptyText    string `json:"empty_text,omitempty"`
	MaxMessages  int    `json:"max_messages,omitempty"`  // trim before render; 0 = unlimited
	RoleField    string `json:"role_field,omitempty"`    // default "role"
	TextField    string `json:"text_field,omitempty"`    // default "text"
	WhoField     string `json:"who_field,omitempty"`     // default "display_name"
	TimeField    string `json:"time_field,omitempty"`    // default "timestamp"
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
	InfoURL    string       `json:"info_url"`
	EventsURL  string       `json:"events_url"`
	CancelURL  string       `json:"cancel_url,omitempty"`
	AppName    string       `json:"app_name,omitempty"`
	TopicField string       `json:"topic_field,omitempty"` // default "topic"
	DoneField  string       `json:"done_field,omitempty"`  // default "done"
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
	Key              string `json:"key"`                          // matches data.stage
	Label            string `json:"label,omitempty"`              // visible text; defaults to capitalized Key
	Icon             string `json:"icon,omitempty"`               // emoji prefix
	SubPattern       string `json:"sub_pattern,omitempty"`        // regex on data.message — match → spawn sub-pill
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
	URL            string `json:"url"`                       // POST returns []{question, hook}
	Method         string `json:"method,omitempty"`          // default POST
	DirectionField string `json:"direction_field,omitempty"` // body field name for the input value (default "direction")
	Placeholder    string `json:"placeholder,omitempty"`
	SuggestLabel   string `json:"suggest_label,omitempty"` // default "Suggest"
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
