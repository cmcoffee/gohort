package ui

import (
	"strings"
)

// SectionSlug names a section for a URL fragment, exactly as the
// section rail does client-side (secnavSlug in 99_epilogue.js): "Try
// it" → "try-it". Server code building a link INTO a sectioned page —
// a diagram node, a shared deep link — must use this, or the two
// transforms drift and the link lands nowhere.
func SectionSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
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

// FieldPreset is one entry in a FormField.Presets list. Label is the
// chip text (e.g. "llama.cpp"); Value is what gets written to the
// input on click (e.g. "http://localhost:8080"). Optional Hint shows
// as the chip's title attribute on hover.
type FieldPreset struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

// SectionSpec is one declared area of a Type=="sections" FormField.
//
// The value such a field saves is still a single markdown string: the
// editor parses it into "## Heading" blocks on load and re-serializes on
// every edit. Nothing server-side needs to know the field is structured
// — prompt builders, exporters, and tools that write the field as plain
// text keep working, and an author can paste raw markdown in.
//
// Mode is only a STARTING shape for an empty section. Once a section has
// content, its editor is chosen by what that content looks like: all
// lines bulleted reads as "list", all lines numbered as "steps",
// anything else as "prose". Switching a section to "list" in the UI
// rewrites its lines as bullets, which parses back as a list next time —
// which is why a per-section choice survives with no schema to store it.
// Content matching no known shape stays prose and is carried verbatim.
type SectionSpec struct {
	// Title is both the heading written into the markdown and the label
	// shown on the slot. Match it exactly to adopt a heading that already
	// exists in the stored value (case-insensitively).
	Title string `json:"title"`
	// Mode is the editor an EMPTY section opens with: "prose" (default),
	// "list", or "steps".
	Mode string `json:"mode,omitempty"`
	// Help renders under the section, like FormField.Help does for an
	// input. Say what belongs in this area.
	Help string `json:"help,omitempty"`
	// Placeholder shows inside the empty editor. Falls back to Help.
	Placeholder string `json:"placeholder,omitempty"`
	// Required marks the slot visually while it's empty. Advisory only —
	// it does not block saving (a sections field saves continuously, the
	// same as every other FormPanel input).
	Required bool `json:"required,omitempty"`

	// AssistPrompt scopes the assist conversation to THIS section, the
	// way FormField.AssistPrompt does for a whole field. Set it where a
	// section wants a different voice from its neighbours — a Rules
	// slot that should produce terse checkable lines reads nothing like
	// a Voice slot. Falls back to the field's AssistPrompt when blank.
	// Same trust note as FormField.AssistPrompt: task instructions only.
	AssistPrompt string `json:"assist_prompt,omitempty"`
}

// SelectOption is one entry in a "select" or "checklist" FormField.
//
// Help and Group are honored by the "checklist" renderer (which can
// show a small subtitle per checkbox and group rows under headings);
// "select" renders only Value + Label.
type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Help  string `json:"help,omitempty"`
	Group string `json:"group,omitempty"`
	// Confirm, when set, asks the user to confirm before this option is
	// applied (select + segmented row actions). Use for a consequential
	// choice — a lockdown level that drops access, an irreversible tier — so
	// picking it isn't a silent one-tap. Empty options apply immediately.
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
	// Data is an opaque value handed to a CLIENT action as
	// ctx.action.data. Method "client" spends URL on the handler's NAME,
	// so without this a client action knows which handler it is and
	// nothing about what it acts on — every app hits this the first time
	// it puts one on a per-item control.
	Data string `json:"data,omitempty"`
	// Group, when set, collapses this action into a "<Group> ▾" dropdown in the
	// toolbar instead of rendering as a standalone button. Actions sharing a
	// Group land in the same menu, in declared order; ungrouped actions stay as
	// flat buttons (the always-visible primaries). Lets a crowded toolbar shed
	// its rarely-used actions into a few overflow menus without per-app JS.
	Group string `json:"group,omitempty"`
	// Prompt makes this a GUIDED action: clicking it types this text into the
	// composer and sends it as the user's turn. URL and Method are ignored —
	// nothing is called, a message is sent, and the reply is an ordinary turn.
	//
	// For surfaces where the useful things to say are a short known list and
	// the person should not have to guess the wording. A chat box is the right
	// control when the next thing to say is open-ended, and the wrong one when
	// there are three of them: "Map this command" is a button, not a sentence
	// somebody should have to compose. The text lands in the conversation
	// verbatim, so write it as the person would say it.
	//
	// Composed with the ordinary composer, never instead of it: the buttons are
	// the way in, and the box is still there for everything after.
	Prompt string `json:"prompt,omitempty"`
}
