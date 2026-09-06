package ui

import (
	"encoding/json"
)

// NavShell is an app-shell layout: a left rail of nav buttons, a content
// pane on the right that swaps to the selected item's Body, and an optional
// Header component pinned at the top of the content pane (always visible
// regardless of selection — e.g. a live activity strip).
//
// Generic by design: any multi-view console (Operator, a future admin
// workbench) composes it from existing components — each NavItem.Body is a
// plain Component (ChatPanel, Table, FormPanel, Stack, ...). The first item
// is selected by default; put the heaviest body (e.g. a ChatPanel) first so
// it mounts visible rather than hidden.
type NavShell struct {
	Toolbar []Component // top control bar, rendered as a horizontal row above everything; nil = no bar
	Header  Component   // pinned at the top of the content pane; nil = no strip
	Items   []NavItem
	// RailSide places the nav rail on the "left" (default) or "right" of the
	// content pane. Right suits a settings surface where the primary page nav
	// already lives on the left and the rail is a secondary within-page index
	// (e.g. the admin Tuning categories).
	RailSide string `json:"rail_side,omitempty"`
	// Embedded switches the shell from filling the viewport (its default,
	// app-shell use like the Operator) to sizing to its content with a cap and
	// internal scroll — for a NavShell dropped INSIDE a page section (below a
	// header + tabs) rather than owning the whole page.
	Embedded bool `json:"embedded,omitempty"`
}

// NavItem is one entry in a NavShell's left rail.
type NavItem struct {
	Label string    // rail button text
	Key   string    // stable id (optional; for deep-linking later)
	Body  Component // rendered in the content pane when selected
}

func (NavShell) componentType() string { return "nav_shell" }

func (n NavShell) MarshalJSON() ([]byte, error) {
	type itemJSON struct {
		Label string          `json:"label"`
		Key   string          `json:"key,omitempty"`
		Body  json.RawMessage `json:"body"`
	}
	items := make([]itemJSON, len(n.Items))
	for i, it := range n.Items {
		items[i] = itemJSON{Label: it.Label, Key: it.Key, Body: marshalComponent(it.Body)}
	}
	var hdr json.RawMessage
	if n.Header != nil {
		hdr = marshalComponent(n.Header)
	}
	var toolbar []json.RawMessage
	for _, c := range n.Toolbar {
		toolbar = append(toolbar, marshalComponent(c))
	}
	return json.Marshal(struct {
		Type     string            `json:"type"`
		Toolbar  []json.RawMessage `json:"toolbar,omitempty"`
		Header   json.RawMessage   `json:"header,omitempty"`
		Items    []itemJSON        `json:"items"`
		RailSide string            `json:"rail_side,omitempty"`
		Embedded bool              `json:"embedded,omitempty"`
	}{"nav_shell", toolbar, hdr, items, n.RailSide, n.Embedded})
}

// Toolbar is a standalone horizontal row of action buttons — the same
// ToolbarAction shape the panels use, but as a top-level Component so any
// page (or a NavShell.Toolbar slot) can host a reusable control bar. Client
// actions dispatch via window.UIClientActions, the same registry the panel
// toolbars use, so a specialized agent's config controls work identically
// wherever the bar is placed.
type Toolbar struct {
	Actions []ToolbarAction `json:"actions"`
}

func (Toolbar) componentType() string { return "toolbar" }

func (t Toolbar) MarshalJSON() ([]byte, error) {
	type alias Toolbar
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"toolbar", alias(t)})
}

// ModalButton renders as an inline button that, on click, pops a
// dialog hosting another component (typically a FormPanel, ChipPicker,
// or Stack of both). The runtime owns the dialog chrome — header,
// subtitle, scrollable body region, Close button, backdrop, escape-to-
// close — so apps just declare what's inside.
//
// Useful for the "chart on main page + button to a settings editor"
// pattern (chart + settings-modal) and any "edit this thing"
// surface that doesn't deserve a full Section but needs more than a
// one-shot ToolbarAction button.
//
// Body is auto-save by default if it's a FormPanel — the embedded
// form handles its own persistence (per-field on blur or on submit).
// No separate save button in the dialog footer; the Close button is
// dismissal, not "commit." Add an explicit FormPanel.SubmitLabel if
// you want the click-to-save shape instead.
type ModalButton struct {
	Label    string    `json:"label"`              // button text (e.g., "Adjust prices")
	Title    string    `json:"title,omitempty"`    // dialog header (defaults to Label)
	Subtitle string    `json:"subtitle,omitempty"` // optional description below title
	Body     Component `json:"-"`                  // dialog content
	Variant  string    `json:"variant,omitempty"`  // button variant (primary, danger, …)
	Width    string    `json:"width,omitempty"`    // dialog max-width CSS (default "520px")
	Align    string    `json:"align,omitempty"`    // "left" | "center" | "right" (default), where the button sits in its row
}

func (ModalButton) componentType() string { return "modal_button" }

func (m ModalButton) MarshalJSON() ([]byte, error) {
	var body json.RawMessage
	if m.Body != nil {
		body = marshalComponent(m.Body)
	}
	return json.Marshal(struct {
		Type     string          `json:"type"`
		Label    string          `json:"label"`
		Title    string          `json:"title,omitempty"`
		Subtitle string          `json:"subtitle,omitempty"`
		Body     json.RawMessage `json:"body,omitempty"`
		Variant  string          `json:"variant,omitempty"`
		Width    string          `json:"width,omitempty"`
		Align    string          `json:"align,omitempty"`
	}{"modal_button", m.Label, m.Title, m.Subtitle, body, m.Variant, m.Width, m.Align})
}
