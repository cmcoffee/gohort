package ui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Head is a typed builder for the app-specific browser behavior a page injects
// into its <head>: client actions, custom block renderers, markdown
// extensions, message decorators, CSS, and (as escape hatches) raw JS / HTML.
//
// It exists so app authors compose extensions in Go instead of hand-writing a
// <script> blob full of window.uiRegister* calls wrapped in a readiness-retry
// guard — the pattern every app used to copy into Page.ExtraHeadHTML. Attach a
// Head to Page.Head; the framework assembles the injection at render time
// (register calls, the retry guard, <style>/<script> wrapping, and </script>
// safety), so no app writes HTML/JS/CSS by hand.
//
// The JS passed to ClientAction / BlockRenderer / MarkdownExtension /
// MessageDecorator is a JS *function expression* — e.g. "function(ctx){ … }".
// Supply it inline or, for anything sizeable, from an embedded assets/ file
// (//go:embed), matching the runtime's own asset-splitting convention. Names
// are JSON-escaped; the JS bodies are trusted, Go-authored, and emitted
// verbatim (same trust model as ExtraHeadHTML).
//
// Build linearly with the fluent methods:
//
//	page.Head = ui.NewHead().
//	    CSS(myCSS).
//	    ClientAction("myapp_greet", greetJS).
//	    BlockRenderer("myapp_plan", planJS)
type Head struct {
	css               []string
	clientActions     []namedJS
	blockRenderers    []namedJS
	markdownExts      []string
	messageDecorators []string
	rawJS             []string
	rawHTML           []string
}

// namedJS pairs a registry key with the JS function expression registered under
// it.
type namedJS struct {
	name string
	fn   string
}

// NewHead returns an empty Head builder.
func NewHead() *Head { return &Head{} }

// CSS appends a stylesheet fragment, injected inside a single <style> block.
func (h *Head) CSS(css string) *Head { h.css = append(h.css, css); return h }

// ClientAction registers a browser handler that a Method:"client" button
// targets by name (Toolbar action, table row action, or ModalButton). fn is a
// JS function expression called with a context object (e.g. {button, action}).
func (h *Head) ClientAction(name, fn string) *Head {
	h.clientActions = append(h.clientActions, namedJS{name, fn})
	return h
}

// BlockRenderer registers a custom renderer for an SSE block type. fn is a JS
// function expression that returns a {wrap, body, onDone?} object.
func (h *Head) BlockRenderer(name, fn string) *Head {
	h.blockRenderers = append(h.blockRenderers, namedJS{name, fn})
	return h
}

// MarkdownExtension registers a markdown post-processor that runs after the
// base render passes. fn is a JS function expression.
func (h *Head) MarkdownExtension(fn string) *Head {
	h.markdownExts = append(h.markdownExts, fn)
	return h
}

// MessageDecorator registers a chat-message decorator. fn is a JS function
// expression.
func (h *Head) MessageDecorator(fn string) *Head {
	h.messageDecorators = append(h.messageDecorators, fn)
	return h
}

// JS appends raw JavaScript run inside the readiness-guarded init block (after
// the registries are known to exist). Escape hatch for behavior the typed
// registrations don't cover.
func (h *Head) JS(js string) *Head { h.rawJS = append(h.rawJS, js); return h }

// HTML appends a raw <head> fragment emitted verbatim, outside the assembled
// <style>/<script>. Escape hatch for legacy full-blob scripts still being
// migrated onto the typed registrations.
func (h *Head) HTML(html string) *Head { h.rawHTML = append(h.rawHTML, html); return h }

// Render assembles the <head> injection: a <style> block (when any CSS was
// added), then a <script> that registers everything inside a readiness-retry
// guard, then any raw HTML fragments. Returns "" when the Head (or receiver) is
// empty, so callers can prepend it to ExtraHeadHTML unconditionally.
func (h *Head) Render() string {
	if h == nil {
		return ""
	}
	var b strings.Builder
	if len(h.css) > 0 {
		b.WriteString("<style>\n")
		b.WriteString(strings.Join(h.css, "\n"))
		b.WriteString("\n</style>\n")
	}
	if h.hasJS() {
		var js strings.Builder
		js.WriteString("(function(){\n  function reg(){\n")
		// The registries are all defined together in the runtime prelude, so
		// gating on one gates them all. The retry covers the (rare) case where
		// this head script somehow runs before the runtime bundle.
		js.WriteString("    if (!window.uiRegisterClientAction) { setTimeout(reg, 30); return; }\n")
		for _, a := range h.clientActions {
			fmt.Fprintf(&js, "    window.uiRegisterClientAction(%s, %s);\n", jsString(a.name), a.fn)
		}
		for _, a := range h.blockRenderers {
			fmt.Fprintf(&js, "    window.uiRegisterBlockRenderer(%s, %s);\n", jsString(a.name), a.fn)
		}
		for _, fn := range h.markdownExts {
			fmt.Fprintf(&js, "    window.uiRegisterMarkdownExtension(%s);\n", fn)
		}
		for _, fn := range h.messageDecorators {
			fmt.Fprintf(&js, "    window.uiRegisterMessageDecorator(%s);\n", fn)
		}
		for _, raw := range h.rawJS {
			js.WriteString(raw)
			js.WriteString("\n")
		}
		js.WriteString("  }\n  reg();\n})();")
		b.WriteString("<script>\n")
		b.WriteString(scriptSafe(js.String()))
		b.WriteString("\n</script>\n")
	}
	for _, raw := range h.rawHTML {
		b.WriteString(raw)
		if !strings.HasSuffix(raw, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// hasJS reports whether any registration would emit a <script>.
func (h *Head) hasJS() bool {
	return len(h.clientActions)+len(h.blockRenderers)+len(h.markdownExts)+
		len(h.messageDecorators)+len(h.rawJS) > 0
}

// jsString encodes s as a safe JS string literal. json.Marshal escapes <, >,
// and & to \u00xx by default, so the result is safe inside a <script>.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// scriptSafe stops a literal "</script" inside a JS string or regex from
// terminating the enclosing <script> element — the same </ escaping the page
// shell applies to the config blob.
func scriptSafe(js string) string {
	return strings.ReplaceAll(js, "</script", "<\\/script")
}
