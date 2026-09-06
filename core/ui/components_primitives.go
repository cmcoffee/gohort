package ui

import (
	"encoding/json"
)

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
	LabelField string `json:"label_field,omitempty"` // default "Label"
	DescField  string `json:"desc_field,omitempty"`  // default "Desc"
	PostTo     string `json:"post_to"`               // e.g. "api/maintenance?key={Label}"
	Method     string `json:"method,omitempty"`      // default POST
	Confirm    string `json:"confirm,omitempty"`
	ButtonText string `json:"button_text,omitempty"` // default "Run"
	EmptyText  string `json:"empty_text,omitempty"`

	// Invalidate — data sources to refresh after a successful action.
	// Matched against other components' Source so a sibling Table
	// refetches itself (via window.uiInvalidate). Use when acting on a
	// row writes something a nearby list shows.
	Invalidate []string `json:"invalidate,omitempty"`
	// ReloadSelf — when true, the list refetches its OWN source after a
	// successful action, dropping the row that was just acted on (e.g. an
	// "add" picker where the chosen item moves into a managed list and
	// should disappear from the picker). Off by default so maintenance
	// lists keep their per-row "done" status visible.
	ReloadSelf bool `json:"reload_self,omitempty"`
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
	Field string `json:"field"`           // field on the parent record
	Title string `json:"title,omitempty"` // optional header above the block
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
	Children []Component       `json:"-"`
	Items    []json.RawMessage `json:"items"`
	// Row lays the children out along one line instead of down the
	// page, wrapping when they run out of room.
	//
	// For a set of children that are ALTERNATIVES to each other — three
	// ways to get a new machine, say. Stacked, each reads as its own
	// step in a sequence; on one line they read as the choice they are.
	Row bool `json:"row,omitempty"`
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
		Row   bool              `json:"row,omitempty"`
	}{"stack", s.Items, s.Row})
}

// Card is a free-form container that just renders raw HTML. Use
// sparingly — escape hatch for things the framework doesn't model yet.
//
// Give it a Source and it stays CURRENT. A card is where a page puts
// what it worked out on the server — a diagram, a plan, a list of
// findings — and that is exactly the content a save on the same page
// invalidates. Without a source the block is computed once and then
// quietly describes the record as it was before the edit that was made
// while looking at it, which is a worse failure than showing nothing:
// it is wrong and it looks fine. Table, DisplayPanel and ChartPanel
// have refetched on invalidation for a while; a card is the same
// problem for content the server renders as HTML.
type Card struct {
	HTML string `json:"html"`
	// Source serves this card's HTML. Answer with the fragment itself
	// (HTML or SVG) or with {"html": "..."} — either is accepted, so an
	// endpoint that already serves a picture can be pointed at directly
	// rather than wrapped.
	//
	// HTML stays the FIRST paint: the server renders the block into the
	// page as before and the source is what keeps it up to date, so a
	// card never flashes "Loading…" on arrival. A card with only a
	// Source fetches on mount.
	Source string `json:"source,omitempty"`
	// RefreshOn names the data whose change should redraw this card,
	// which is rarely the card's own Source: a diagram of the stages is
	// refetched when a STAGE is saved. Entries match an invalidated
	// source by PREFIX, so "api/things/7/parts" covers the per-record
	// writes to "api/things/7/parts?name=x" that a form broadcasts.
	// Source itself always refreshes the card, listed here or not.
	RefreshOn []string `json:"refresh_on,omitempty"`
	// AutoRefreshMS re-fetches Source on an interval, for a block that
	// changes on its own rather than when the reader does something.
	// Paused while the tab is hidden (see uiAutoRefresh).
	AutoRefreshMS int `json:"auto_refresh_ms,omitempty"`
}

func (Card) componentType() string { return "card" }

func (c Card) MarshalJSON() ([]byte, error) {
	type alias Card
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"card", alias(c)})
}

// Frame renders a COMPLETE, self-contained HTML document in its own iframe
// (via srcdoc) instead of splicing it into the host page. Use it whenever the
// HTML is a whole document rather than a fragment — a game, a canvas
// animation, a simulation, an embedded mini-app.
//
// The distinction matters because a Card inlines its HTML: the browser drops
// the document's <html>/<head>/<body> wrappers but KEEPS its <style>, so a
// blob that opens with the usual `* { margin: 0 }` reset and a `body { … }`
// rule silently restyles the entire surrounding page, and its `100vh` layout
// measures the whole window rather than its own box. A Frame gives the
// document its own viewport, its own cascade, and its own <body> to measure.
//
// NOT a sandbox: the frame keeps the parent's origin (no sandbox attribute),
// because a framed document is the same owner-authored, owner-served content a
// Card holds — it must keep the relative fetches, cookies, and storage the
// page it came from has. Frame is about ISOLATING LAYOUT, not privilege. For
// untrusted or model-authored HTML shown to a user, the sandboxed artifact
// pane (uiOpenArtifactPane) is the surface with the trust boundary.
type Frame struct {
	HTML string `json:"html"`
	// Height is any CSS length for the frame box (e.g. "640px", "80vh").
	// Defaults to a tall-but-bounded viewport when empty.
	Height string `json:"height,omitempty"`
}

func (Frame) componentType() string { return "frame" }

func (f Frame) MarshalJSON() ([]byte, error) {
	type alias Frame
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"frame", alias(f)})
}

// Button is a single action button that calls an endpoint on click — the
// standalone-component form of a row-action button, so a button can live
// inside a Stack / Expand panel (which only accept nested Components) rather
// than only in a Table's RowActions. When mounted inside a row expander it
// receives the same {row_key} URL substitution and row-record context as the
// other nested components, so OnlyIf / HideIf can gate it on a row field
// (e.g. show "Clear" only when the row is connected). Standalone (no row
// context) buttons ignore the gates. On success it can refresh other lists
// (Invalidate, matched against their Source) and surface a server {message}.
type Button struct {
	Label      string   `json:"label"`
	URL        string   `json:"url"`                  // endpoint to call
	Method     string   `json:"method,omitempty"`     // default POST
	Confirm    string   `json:"confirm,omitempty"`    // confirmation prompt before firing
	Invalidate []string `json:"invalidate,omitempty"` // Sources to refetch on success
	Variant    string   `json:"variant,omitempty"`    // extra CSS class, e.g. "danger"
	// OnlyIf / HideIf gate rendering on a row-record field when the button is
	// nested in a row expander: render only when OnlyIf is truthy / hide when
	// HideIf is truthy. Ignored when there's no row context.
	OnlyIf string `json:"only_if,omitempty"`
	HideIf string `json:"hide_if,omitempty"`
}

func (Button) componentType() string { return "button" }

func (b Button) MarshalJSON() ([]byte, error) {
	type alias Button
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"button", alias(b)})
}

// EmptyState is the centered "nothing here yet" placeholder — a large dimmed
// Icon, a Title, an optional Hint, and an optional call-to-action button. Use
// it as a Section.Body (or a panel's body) for a surface that has no content
// yet, instead of a bare EmptyText string. The considered empty state is a
// deliberate default: every list/viewer surface should have one.
type EmptyState struct {
	Icon         string `json:"icon,omitempty"`          // shown large + dimmed, e.g. "📖"
	Title        string `json:"title"`                   // primary line, e.g. "No guide selected"
	Hint         string `json:"hint,omitempty"`          // secondary guidance line
	ActionLabel  string `json:"action_label,omitempty"`  // optional CTA button text
	ActionURL    string `json:"action_url,omitempty"`    // CTA target (relative or absolute)
	ActionMethod string `json:"action_method,omitempty"` // "GET" (navigate, default) | "POST" (then reload)
}

func (EmptyState) componentType() string { return "empty_state" }

func (e EmptyState) MarshalJSON() ([]byte, error) {
	type alias EmptyState
	return json.Marshal(struct {
		Type string `json:"type"`
		alias
	}{"empty_state", alias(e)})
}
