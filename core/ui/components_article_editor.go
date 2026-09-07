package ui

import (
	"encoding/json"
)

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
	LoadURL   string `json:"load_url"` // template with {id}
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
	// ListLabel names the sidebar list (its header + the mobile drawer
	// title). Default "Articles"; override to match the record kind the
	// editor holds.
	ListLabel string `json:"list_label,omitempty"`
	// NoNew hides the "+ New" button — for an editor over a FIXED set of
	// records (you edit the existing items, you don't create new ones, e.g.
	// a fixed catalog of records).
	NoNew bool `json:"no_new,omitempty"`
	// NoSearch hides the sidebar search box — for a small fixed list where
	// a search field is just noise.
	NoSearch bool `json:"no_search,omitempty"`
	// NoCollapse removes the sidebar collapse control (and the floating
	// expand tab that brings it back) — for a list that is short and always
	// relevant, where hiding it buys nothing and the control is just one more
	// thing on screen. Also ignores any collapsed state another editor left in
	// localStorage, so the list can never open hidden.
	NoCollapse bool `json:"no_collapse,omitempty"`
	// TitleReadOnly makes the title input read-only — the record's NAME is
	// fixed (framework-defined), so the user edits the body, not the title
	// (e.g. a record whose name is its key).
	TitleReadOnly bool `json:"title_readonly,omitempty"`

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
	// ReferenceSourcesURL, when set, renders a generic reference picker in
	// the chat pane. GET → []core.ReferenceGroup
	// ({kind, label, items:[{id, name, desc}]}). The selected item rides
	// with each chat request as `references` ([{kind, item_id}]); the app's
	// ChatURL handler injects that source's text into the model context.
	// Domain-agnostic — see core.ReferenceSource / RegisterReferenceSource.
	ReferenceSourcesURL string `json:"reference_sources_url,omitempty"`
	// ImageField is the JSON field name on the article record that
	// holds the header image URL. Default "ImageURL". Set blank to
	// disable image persistence (the editor still surfaces images
	// supplied by client actions but won't round-trip them).
	// Outline turns on the sectioned markdown view over the body: an
	// "Outline" toggle that presents the document as headed blocks
	// (core/ui's sections editor) instead of one textarea. The textarea
	// stays the value carrier, so save / chat / merge / revisions are
	// unaffected. Opt-in because not every ArticleEditor body is
	// markdown worth sectioning.
	Outline bool `json:"outline,omitempty"`

	// Templates offers starting skeletons via a "Templates" button.
	// TemplatesListURL / TemplateURL add the user's own saved templates
	// on top (GET/POST the list, DELETE one) — same contract as
	// CodeEditorPanel's. Leave all three unset to hide the button.
	Templates        []DocTemplate `json:"templates,omitempty"`
	TemplatesListURL string        `json:"templates_list_url,omitempty"`
	TemplateURL      string        `json:"template_url,omitempty"`

	// AssistURL turns on the draft-with-me workbench: the document
	// beside a conversation, with a walk back through earlier versions.
	// A ✨ appears per section in the outline, and a whole-document one
	// in raw view. POST {name, section, message, draft, history} →
	// {reply, value}; section is "" for the whole document.
	AssistURL string `json:"assist_url,omitempty"`

	ImageField string `json:"image_field,omitempty"`

	// ExtraActions populates a "More ▾" popover at the right end of
	// the toolbar with app-defined actions. See MenuAction for the
	// shape.
	ExtraActions []MenuAction `json:"extra_actions,omitempty"`

	// ListActions renders buttons in the sidebar LIST header — for
	// actions scoped to the whole collection rather than the open record
	// (e.g. a batch action over the whole list). Same ToolbarAction
	// dispatch as Actions; they just live with the list, not the editor.
	ListActions []ToolbarAction `json:"list_actions,omitempty"`

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

func (ArticleEditor) componentType() string { return "article_editor" }

func (a ArticleEditor) MarshalJSON() ([]byte, error) {
	type alias ArticleEditor
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"article_editor", alias(a)})
}
