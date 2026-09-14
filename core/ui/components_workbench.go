package ui

import (
	"encoding/json"
)

// WorkbenchPanel is the three-column document-workbench layout: an item LIST
// (left), a markdown VIEWER of the selected item (center), and a CHAT bound to
// an agent (right). It owns the shared selection state the three columns lack on
// their own — clicking a list row loads that record into the viewer. The chat is
// any Component (typically an AgentLoopPanel); the New affordance is any
// Component (typically a ModalButton wrapping a FormPanel) mounted in the list
// header. Generic: any "browse a list → read the selected doc → talk to an
// assistant about it" surface uses it, not just training guides.
//
// The viewer renders record[BodyField] as markdown. The co-author flow (the
// chat's agent appending to that field so the viewer updates) is wired by the
// host app via a tool on the bound agent + RefreshOn; this component just
// re-fetches the open record when told to.
type WorkbenchPanel struct {
	// Left — item list.
	ListURL   string `json:"list_url"`             // GET → [records]
	ItemKey   string `json:"item_key,omitempty"`   // record id field (default "id")
	ItemLabel string `json:"item_label,omitempty"` // record label field (default "title")
	ListTitle string `json:"list_title,omitempty"` // column header (default "Items")
	ListEmpty string `json:"list_empty,omitempty"` // empty-list text
	// NewButton mounts in the list header (typically a ModalButton+FormPanel
	// whose FormPanel posts to ListURL and Invalidates it so the list refreshes).
	NewButton Component `json:"-"`
	// DeleteURL — when set, each list row gets a delete affordance (✕). DELETE to
	// this URL with {id} substituted; the list refreshes and the viewer clears if
	// the open record was the one removed.
	DeleteURL string `json:"delete_url,omitempty"`
	// Center — viewer of the selected record.
	RecordURL        string `json:"record_url"`                   // GET with {id} → the record
	BodyField        string `json:"body_field,omitempty"`         // field rendered in the viewer (default "content")
	BodyIsHTML       bool   `json:"body_is_html,omitempty"`       // render BodyField as trusted server HTML (innerHTML) instead of markdown — for apps that render a rich document (ToC + sections) server-side
	ViewerTitleField string `json:"viewer_title_field,omitempty"` // optional record field shown as a heading
	EmptyIcon        string `json:"empty_icon,omitempty"`
	EmptyTitle       string `json:"empty_title,omitempty"`
	EmptyHint        string `json:"empty_hint,omitempty"`
	// EditURL turns on direct editing of the open record: the viewer toolbar
	// gains an Edit toggle that swaps the rendered document for a textarea over
	// the record's EditField — the markdown SOURCE, which for a BodyIsHTML app
	// is a different field from the rendered BodyField. Save POSTs
	// {id, <EditField>: text} to EditURL ({id} substituted) and re-fetches the
	// record; Cancel discards. The toggle stays disabled for a record whose
	// EditField is absent (not a string), so an app can offer direct editing
	// for some kinds of document and leave others to their own controls. The
	// viewer holds off its refresh while the textarea is open, so a co-author
	// write landing mid-edit cannot wipe what is being typed; the next Save
	// wins, and History keeps the rest.
	EditURL   string `json:"edit_url,omitempty"`
	EditField string `json:"edit_field,omitempty"` // default "markdown"
	EditLabel string `json:"edit_label,omitempty"` // toggle text (default "Edit")
	// ViewerActions render as a button row above the document — actions on the
	// SELECTED record (export, history, audit, …). Generic: any workbench can add
	// per-document actions without core knowing what they do.
	ViewerActions []WorkbenchAction `json:"viewer_actions,omitempty"`
	// ListActions render in the LIST header, to the left of the New button — for
	// record-scoped actions that belong with the list rather than the document
	// toolbar (e.g. Edit the selected record's settings). Same WorkbenchAction
	// dispatch as ViewerActions; enabled only when a record is selected.
	ListActions []WorkbenchAction `json:"list_actions,omitempty"`
	// RefreshOn — when uiInvalidate fires with a source in this list, the open
	// record re-fetches (so a co-author write shows up without a manual reload).
	RefreshOn []string `json:"refresh_on,omitempty"`
	// ActiveURL — when set, the panel POSTs {id} here whenever the open record
	// changes, so the server (and the chat agent's co-author tool) knows which
	// document is open. The viewer also re-fetches the open record whenever the
	// embedded chat finishes a round (the agent may have written into it).
	ActiveURL string `json:"active_url,omitempty"`
	// CoAuthor — when set, each assistant chat reply gets an "Add to <noun>"
	// button that APPENDS that reply's markdown to the open record's BodyField
	// (fetch RecordURL, append, POST SaveURL as an upsert) and refreshes the
	// viewer. The co-author flow: ask the assistant for a section, then commit it
	// into the open document.
	CoAuthor     bool   `json:"coauthor,omitempty"`
	CoAuthorVerb string `json:"coauthor_verb,omitempty"` // button text (default "Add to document")
	SaveURL      string `json:"save_url,omitempty"`      // POST (upsert) the modified record; default = ListURL
	// Right — chat (typically an AgentLoopPanel or single ChatPanel). Mounted as-is.
	Chat Component `json:"-"`
}

// WorkbenchAction is one button in a WorkbenchPanel's viewer toolbar, acting on
// the selected record. {id} in URL/RestoreURL is substituted with the record id.
//
// Kind:
//   - "download" — open URL in a new tab (browser downloads, or previews HTML).
//   - "report"   — POST URL, render the returned {report} markdown in a modal
//     (e.g. an audit). Spinner shows while it runs. The JSON response MAY also
//     carry an optional {apply} action ({label, url, spinner, confirm,
//     invalidate}) — the modal renders it as a button that POSTs {report: <the
//     report markdown>} to apply.url, then invalidates and replaces the modal
//     with the returned summary. This lets a read-only report (an audit) offer a
//     one-click apply without re-deriving its findings, while staying generic.
//   - "history"  — GET URL → [{id, at, note}]; render a list with Restore
//     buttons that POST RestoreURL (with {id} = record, {rev} = entry id), then
//     refresh the viewer. When PreviewURL is also set each entry gets a View
//     button that GETs it ({id}/{rev} substituted the same way) and shows that
//     snapshot read-only — the common reason to open history is not to roll the
//     document back but to read one paragraph that went missing and copy it
//     forward, and restoring to do that would throw away everything written
//     since.
//   - "client"   — browser-side action: URL carries the name of a handler
//     registered via window.uiRegisterClientAction. The handler receives
//     ({recordId, button, action, refresh}) so an app can mount its own toolbar
//     behavior (open a picker, copy, print, …) without core/ui knowing it.
type WorkbenchAction struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Kind  string `json:"kind"`
	// Scope says what the action is ABOUT. The default ("" or "record") is the
	// open record, so the button follows the selection and greys out when there
	// is none. "library" is about the collection itself — it stays enabled with
	// nothing selected, because a selection was never a precondition for it.
	//
	// Both bars used to gate everything in them on a selection, which made the
	// two actions that matter most on an EMPTY library — import something into
	// it, set the rules it is written under — the two that could not be clicked.
	// Scope also decouples where an action SITS from when it is usable, so a
	// library action can live in the viewer bar beside the record ones without
	// inheriting their gating.
	Scope      string `json:"scope,omitempty"`
	RestoreURL string `json:"restore_url,omitempty"`
	// PreviewURL — "history" only. GET ({id}/{rev} substituted) → {title?, html?,
	// markdown?}; rendered read-only in a modal. html is server-built and trusted
	// (same posture as WorkbenchPanel.BodyIsHTML), so the app owns what it says —
	// including any marking of what this version has that the current one lost.
	PreviewURL string `json:"preview_url,omitempty"`
	Confirm    string `json:"confirm,omitempty"`
	Spinner    string `json:"spinner,omitempty"` // busy label for "report" (default "Working…")
	// Invalidate — for a "report" action that CHANGES the open record (e.g. an
	// LLM pass that rewrites sections), the sources to uiInvalidate after it
	// finishes so the viewer/list refresh. Empty = show the report only.
	Invalidate []string `json:"invalidate,omitempty"`
	// Compose — Kind == "compose" only. The message this button puts in the
	// chat composer, for a control that is really a macro: a fixed prompt with
	// a label on it.
	//
	// It seeds, it does NOT send. A "report" action POSTs a prompt the user
	// cannot read, cannot adjust and cannot learn from; this one hands them the
	// same default with the cursor after it, so one Enter is the old behavior
	// and any edit is the behavior it never had. What the button was going to
	// say stops being invisible.
	//
	// {id} is substituted with the open record's id, the same as URL — a prompt
	// usually has to name what it is about.
	//
	// Needs a chat column on the page (WorkbenchPanel.Chat). Without one there
	// is no composer to seed and the button reports that rather than failing
	// quietly.
	Compose string `json:"compose,omitempty"`
	// ComposeOptions turn a compose action into a short CHOOSER: a modal listing
	// the ways this control is usually used, and a send.
	//
	// It exists because seeding alone got the common case wrong. The default is
	// what the user wants nearly every time, and making them read it and press
	// Enter taxes every single use to buy an edit they rarely need. A chooser
	// pays the opposite way round: the default is one click, and the escape
	// hatch — an option with Input set — is right there for the time they want
	// something else, which is more discoverable than a pre-filled box they have
	// to notice is editable.
	//
	// The chosen text is sent, not left sitting in the composer. Asking someone
	// what they want and then making them confirm the same intent again is the
	// friction this replaces, not a safety step.
	//
	// Empty = seed the composer with Compose and stop, which is right for a
	// control with exactly one sensible instruction and nothing to choose
	// between.
	ComposeOptions []ComposeOption `json:"compose_options,omitempty"`
	// ComposeTitle heads the chooser. Defaults to the action's Label.
	ComposeTitle string `json:"compose_title,omitempty"`
	// Children, when Kind == "menu", are the sub-actions shown in a dropdown when
	// the button is clicked — e.g. an "Export" button grouping HTML / PDF /
	// Markdown downloads so related actions don't crowd the toolbar. Each child is
	// a normal WorkbenchAction dispatched by its own Kind (download / client /
	// report / …). Ignored for non-menu kinds.
	Children []WorkbenchAction `json:"children,omitempty"`
}

// ComposeOption is one row in a compose action's chooser.
//
// Text is the message that option sends. Input makes the row a free-text one
// instead: the user types the message, with Text as the starting value, so
// "like the default but…" costs an edit rather than a retype. An Input row with
// no Text is an empty box — the plain "something else" escape.
type ComposeOption struct {
	Label       string `json:"label"`
	Help        string `json:"help,omitempty"` // one line under the label
	Text        string `json:"text,omitempty"`
	Input       bool   `json:"input,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

func (WorkbenchPanel) componentType() string { return "workbench_panel" }

func (w WorkbenchPanel) MarshalJSON() ([]byte, error) {
	var newBtn, chat json.RawMessage
	if w.NewButton != nil {
		newBtn = marshalComponent(w.NewButton)
	}
	if w.Chat != nil {
		chat = marshalComponent(w.Chat)
	}
	type alias WorkbenchPanel
	return json.Marshal(struct {
		Type      string          `json:"type"`
		NewButton json.RawMessage `json:"new_button,omitempty"`
		Chat      json.RawMessage `json:"chat,omitempty"`
		alias
	}{"workbench_panel", newBtn, chat, alias(w)})
}
