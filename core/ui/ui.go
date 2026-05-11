// Package ui provides a declarative component framework for gohort web
// apps. Apps describe a page as a Go struct tree (Page → Sections →
// Components); the framework renders an HTML shell with a JSON config
// blob, and a single shared runtime (ui.js + ui.css) hydrates the page
// in the browser. App authors never write HTML, CSS, or JS directly —
// new apps describe their UI in pure Go.
//
// The shared runtime handles iOS-style switches, sticky bars, tables
// with row actions, auto-refresh, pull-to-refresh, modals, toasts, and
// the gohort theme tokens (Blackboard scheme by default). Per-component
// static state is in the JSON blob; per-component handlers are URLs
// the runtime fetches with the right method/body.
//
// Usage in an app:
//
//	mux.HandleFunc("/myapp/", func(w http.ResponseWriter, r *http.Request) {
//	    ui.Page{
//	        Title: "My App",
//	        Sections: []ui.Section{
//	            {Title: "Settings", Body: ui.ToggleGroup{
//	                Source: "api/config",
//	                Toggles: []ui.Toggle{{Field: "enabled", Label: "On"}},
//	            }},
//	        },
//	    }.ServeHTTP(w, r)
//	})
//
// The runtime endpoints (/_ui/ui.css, /_ui/ui.js) are registered by
// MountRuntime, called once at server startup.
package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/webui"
)

// Component is anything that can describe itself as a runtime
// configuration object. The runtime JS dispatches on the "type" field
// to pick the right hydration logic. Implementations live alongside
// their UI definitions in components.go.
type Component interface {
	componentType() string
}

// Page is the top-level UI declaration. Render emits an HTML document
// that loads ui.css + ui.js and embeds a JSON config the runtime reads
// at startup.
type Page struct {
	// Title for the <title> tag and (when ShowHeader is true) the page header.
	Title string
	// BackURL renders a "← Back" link at the top-left of the page.
	// Empty omits the back link. Set to "/" to return to the gohort
	// app menu, or to any path the operator wants.
	BackURL string
	// ShowTitle renders the Title visibly at the top of the page (in
	// addition to the document <title>). Pairs with BackURL — the
	// header bar shows back-arrow + title + optional badge area.
	ShowTitle bool
	// Sticky is rendered above all sections, sticky to the top of the
	// viewport. Typically a PanicBar or AlertBar.
	Sticky Component
	// Sections are rendered in order. Most apps have 2-5.
	Sections []Section
	// MaxWidth caps the central column width. Default 600px (mobile-first
	// even on desktop). Set to "" or "100%" for full width.
	MaxWidth string
	// Theme override. "" uses the default Blackboard tokens.
	Theme string
	// Footer text or a link, rendered centered below the last section.
	Footer string
	// FooterURL turns Footer into a link.
	FooterURL string
	// ExtraHeadHTML is injected into the <head> verbatim. Use sparingly
	// — escape hatch for apps that need to bring in legacy CSS/JS that
	// hasn't been ported into the framework yet (e.g. codewriter's
	// inline diff renderer from core/editor). Trusted strings only;
	// the framework does not escape this.
	ExtraHeadHTML string
}

// ServeHTTP renders the Page to w. Apps wire it directly into HandleFunc.
func (p Page) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := p.Render(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Render writes the full HTML document for the page.
func (p Page) Render(w http.ResponseWriter) error {
	cfg := pageConfig{
		Title:    p.Title,
		Sticky:   marshalComponent(p.Sticky),
		Sections: make([]sectionConfig, 0, len(p.Sections)),
		MaxWidth: p.MaxWidth,
		Footer:   p.Footer,
		FooterURL: p.FooterURL,
		BackURL:  p.BackURL,
		ShowTitle: p.ShowTitle,
	}
	if cfg.MaxWidth == "" {
		cfg.MaxWidth = "600px"
	}
	for _, s := range p.Sections {
		cfg.Sections = append(cfg.Sections, sectionConfig{
			Title:    s.Title,
			Subtitle: s.Subtitle,
			Body:     marshalComponent(s.Body),
			NoChrome: s.NoChrome,
		})
	}
	jsonBlob, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("ui: marshal page: %w", err)
	}
	theme := p.Theme
	if theme == "" {
		theme = "blackboard"
	}
	fmt.Fprintf(w, `<!doctype html>
<html lang="en" data-theme=%q>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>%s</title>
<link rel="stylesheet" href="/_ui/ui.css">
%s</head>
<body>
<div id="ui-root"></div>
<script id="ui-config" type="application/json">%s</script>
<script src="/_ui/ui.js"></script>
</body>
</html>`, theme, htmlEscape(p.Title), p.ExtraHeadHTML, strings.ReplaceAll(string(jsonBlob), "</", "<\\/"))
	return nil
}

// MountRuntime registers the shared CSS and JS endpoints on the given
// mux. Call once at startup before any Page handlers are wired.
//
// The runtime assets are served with no-cache + an ETag derived from
// the content hash. This means each page load makes a small
// conditional request that returns 304 when nothing changed, but the
// instant the binary's runtime CSS/JS does change (after a rebuild),
// the new bytes are picked up — no stale assets in browser caches
// across iterations of the framework.
func MountRuntime(mux *http.ServeMux) {
	// Prepend the Orbitron @font-face declaration so the metallic
	// gradient page-title (font-family: Orbitron) actually resolves.
	// Without this the title falls back to a system font and loses
	// the wordmark look the legacy webui chrome has.
	cssBlob := webui.FontFaceCSS() + runtimeCSS
	cssETag := `"` + assetETag(cssBlob) + `"`
	jsETag := `"` + assetETag(runtimeJS) + `"`
	mux.HandleFunc("/_ui/ui.css", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, "text/css; charset=utf-8", cssETag, cssBlob)
	})
	mux.HandleFunc("/_ui/ui.js", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, "application/javascript; charset=utf-8", jsETag, runtimeJS)
	})
}

func serveAsset(w http.ResponseWriter, r *http.Request, ct, etag, body string) {
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Write([]byte(body))
}

// assetETag returns a short hex tag derived from the asset content. We
// only need uniqueness across rebuilds, not cryptographic strength —
// FNV-1a-32 is fine and avoids importing crypto packages here.
func assetETag(s string) string {
	const offset = 2166136261
	const prime = 16777619
	var h uint32 = offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return fmt.Sprintf("%08x", h)
}

// Section groups related components under a titled card. NoChrome
// drops the card padding, background, and border — useful when a
// component manages its own layout (e.g. ChatPanel's two-pane grid)
// and the section wrap would just add visual noise around it.
type Section struct {
	Title    string
	Subtitle string
	Body     Component
	NoChrome bool
}

// --- internal: serialization shape consumed by ui.js ----------------------

type pageConfig struct {
	Title     string          `json:"title"`
	BackURL   string          `json:"back_url,omitempty"`
	ShowTitle bool            `json:"show_title,omitempty"`
	Sticky    json.RawMessage `json:"sticky,omitempty"`
	Sections  []sectionConfig `json:"sections"`
	MaxWidth  string          `json:"max_width"`
	Footer    string          `json:"footer,omitempty"`
	FooterURL string          `json:"footer_url,omitempty"`
}

type sectionConfig struct {
	Title    string          `json:"title"`
	Subtitle string          `json:"subtitle,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	NoChrome bool            `json:"no_chrome,omitempty"`
}

// marshalComponent serializes a Component as JSON with a "type" tag the
// runtime dispatches on. Returns null when c is nil.
func marshalComponent(c Component) json.RawMessage {
	if c == nil {
		return json.RawMessage("null")
	}
	// Use reflection-free path: every component implements MarshalJSON
	// to inject its own type tag. See components.go.
	b, err := json.Marshal(c)
	if err != nil {
		return json.RawMessage(fmt.Sprintf("{\"type\":\"error\",\"message\":%q}", err.Error()))
	}
	return b
}

// htmlEscape minimally escapes for the page <title>.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}
