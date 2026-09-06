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
	Label      string `json:"label"`
	URL        string `json:"url"`
	Kind       string `json:"kind"`
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
	// Children, when Kind == "menu", are the sub-actions shown in a dropdown when
	// the button is clicked — e.g. an "Export" button grouping HTML / PDF /
	// Markdown downloads so related actions don't crowd the toolbar. Each child is
	// a normal WorkbenchAction dispatched by its own Kind (download / client /
	// report / …). Ignored for non-menu kinds.
	Children []WorkbenchAction `json:"children,omitempty"`
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
