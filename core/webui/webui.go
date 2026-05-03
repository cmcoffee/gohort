// Package webui provides shared web UI scaffolding for gohort apps.
//
// Goal: eliminate duplicated CSS, JS, and HTML chrome across web
// frontends. Each app supplies its own body HTML, app-specific CSS,
// and JS, then calls RenderPage to assemble a complete document with the
// shared theme, utilities, and access-control hooks already wired in.
//
// All static assets are embedded via go:embed, so the binary remains
// single-file. Apps can also serve raw assets via /webui/<file> if they
// prefer browser-cached external resources later.
package webui

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
)

//go:embed static
var staticFS embed.FS

// asset returns the contents of an embedded static file. Panics on
// missing files because asset names are compile-time constants.
func asset(name string) string {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		panic("webui: missing asset " + name + ": " + err.Error())
	}
	return string(b)
}

// BaseCSS returns the shared dark theme CSS used by all apps. Apps may
// append their own styles via PageOpts.AppCSS for app-specific tweaks.
// Includes self-hosted font @font-face declarations so app headers can
// reference 'Space Grotesk' without loading anything external.
func BaseCSS() string { return fontFaceCSS() + asset("base.css") }

var (
	fontFaceOnce sync.Once
	fontFaceStr  string
)

// FontFaceCSS returns the @font-face declaration for Orbitron Bold
// (700), with the font binary inlined as a base64 data URL. No
// external HTTP request and no dependency on ServeAssets being wired.
// Size: ~9KB of base64 text. Loaded once and memoized.
//
// Exposed for apps that render their own HTML templates (without
// going through RenderPage) — they can inject this into their own
// <style> block or <head> to pick up the shared header font.
func FontFaceCSS() string { return fontFaceCSS() }

func fontFaceCSS() string {
	fontFaceOnce.Do(func() {
		raw, err := staticFS.ReadFile("static/fonts/orbitron-700-latin.woff2")
		if err != nil {
			// Missing font asset is non-fatal — pages render with
			// system font fallback. Log via panic-on-asset is too
			// strong for a stylistic enhancement.
			fontFaceStr = ""
			return
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		fontFaceStr = "@font-face{" +
			"font-family:'Orbitron';" +
			"font-style:normal;" +
			"font-weight:700;" +
			"font-display:swap;" +
			"src:url(data:font/woff2;base64," + encoded + ") format('woff2');" +
			"unicode-range:U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD;" +
			"}\n"
	})
	return fontFaceStr
}

// BaseJS returns the shared utility JS — access-control check that
// hides Push-to-TechWriter buttons, live session ribbon polling, and
// small DOM helpers. Apps append pipeline-specific JS via PageOpts.AppJS.
func BaseJS() string { return asset("base.js") }

// PageOpts describes a single page render. Required fields: Title,
// AppName, BodyHTML. Everything else is optional.
type PageOpts struct {
	Title    string // <title> contents
	AppName  string // app label (e.g. "MyApp") shown in headers/ribbons
	BodyHTML string // app-specific HTML body (main panel)
	AppCSS   string // app-specific CSS appended after BaseCSS
	AppJS    string // app-specific JS appended after BaseJS
	InitJS   string // optional inline init script run on DOMContentLoaded
	HeadHTML string // extra <head> content (link/script tags) injected before </head>
	Prefix   string // mount prefix (e.g. "/techwriter") — empty for standalone
}

// RenderPage assembles a full HTML document from the shared theme plus
// the app-specific pieces. Returns the document as a string ready to
// write to an http.ResponseWriter.
//
// When opts.Prefix is set, RenderPage injects a <base href> so the
// document's relative API paths resolve correctly under the mounted
// prefix, and renders a floating back-to-dashboard button in the
// upper-left corner.
func RenderPage(opts PageOpts) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover">
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;utf8,`)
	sb.WriteString(faviconSVG)
	sb.WriteString(`">
`)
	if opts.Prefix != "" {
		sb.WriteString(`<base href="`)
		sb.WriteString(html.EscapeString(opts.Prefix + "/"))
		sb.WriteString("\">\n")
	}
	sb.WriteString("<title>")
	sb.WriteString(html.EscapeString(opts.Title))
	sb.WriteString(`</title>
<style>
`)
	sb.WriteString(BaseCSS())
	if opts.Prefix != "" {
		sb.WriteString("\n/* --- back-to-dashboard chrome --- */\n")
		sb.WriteString(backChromeCSS)
	}
	if opts.AppCSS != "" {
		sb.WriteString("\n/* --- app-specific --- */\n")
		sb.WriteString(opts.AppCSS)
	}
	sb.WriteString("\n</style>\n")
	if opts.HeadHTML != "" {
		sb.WriteString(opts.HeadHTML)
		sb.WriteString("\n")
	}
	sb.WriteString(`</head>
<body data-app="`)
	sb.WriteString(html.EscapeString(opts.AppName))
	sb.WriteString(`">
`)
	if opts.Prefix != "" {
		sb.WriteString(backChromeHTML)
	}
	body := strings.ReplaceAll(opts.BodyHTML, "{{AppName}}", html.EscapeString(opts.AppName))
	if opts.Prefix != "" {
		// Convert absolute /api/ paths to relative so <base href> resolves
		// them. Same transform ServeHTMLWithBase historically did.
		body = strings.ReplaceAll(body, "'/api/", "'api/")
		body = strings.ReplaceAll(body, "\"/api/", "\"api/")
	}
	sb.WriteString(body)
	sb.WriteString(`
<script>
`)
	sb.WriteString(BaseJS())
	if opts.AppJS != "" {
		sb.WriteString("\n// --- app-specific ---\n")
		appJS := opts.AppJS
		if opts.Prefix != "" {
			appJS = strings.ReplaceAll(appJS, "'/api/", "'api/")
			appJS = strings.ReplaceAll(appJS, "\"/api/", "\"api/")
		}
		sb.WriteString(appJS)
	}
	if opts.InitJS != "" {
		sb.WriteString("\ndocument.addEventListener('DOMContentLoaded', function(){\n")
		sb.WriteString(opts.InitJS)
		sb.WriteString("\n});\n")
	}
	sb.WriteString(`
</script>
</body>
</html>`)
	return sb.String()
}

// faviconSVG is a small inline SVG favicon used in the <link rel="icon">
// data URI. URL-encoded so it can be embedded directly in the href without
// further escaping. Renders as a "G" monogram in gohort's accent blue on
// a dark rounded background — matches the dashboard theme.
const faviconSVG = "%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E" +
	"%3Crect width='64' height='64' rx='12' fill='%230d1117'/%3E" +
	"%3Ctext x='50%25' y='50%25' dominant-baseline='central' text-anchor='middle' " +
	"font-family='-apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,sans-serif' " +
	"font-size='40' font-weight='700' fill='%2358a6ff'%3EG%3C/text%3E" +
	"%3C/svg%3E"

// Floating back-to-dashboard button rendered when Prefix is set.
const backChromeCSS = `
#dashboard-back {
  position: fixed; top: 12px; left: 12px; z-index: 9999;
  display: inline-flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 6px;
  background: #161b22; border: 1px solid #30363d;
  color: #8b949e; text-decoration: none; font-size: 1rem;
  transition: border-color 0.2s, color 0.2s, background 0.2s;
}
#dashboard-back:hover { border-color: #58a6ff; color: #f0f6fc; background: #1c2128; }
body { padding-top: 3.5rem; }
`

// Back arrow: default navigates to the dashboard (href="/"). Apps
// with drilled-in views can set window.drillBackHandler to a function;
// when set, clicking calls that function instead of navigating so the
// arrow returns to the app's list view (equivalent to clicking the
// app's in-arena "Back to Reports" / "Back to Debates" button). Apps
// clear the handler on drill-out so the arrow reverts to
// dashboard-navigation. Same onclick as ServeHTMLWithBase's back
// button so all apps (webui-framework + raw-HTML) behave identically.
const backChromeHTML = `<a id="dashboard-back" href="/" title="Back" onclick="if(typeof window.drillBackHandler==='function'){window.drillBackHandler();return false;}return true;">
<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M7.78 12.53a.75.75 0 01-1.06 0L2.47 8.28a.75.75 0 010-1.06l4.25-4.25a.75.75 0 011.06 1.06L4.81 7h7.44a.75.75 0 010 1.5H4.81l2.97 2.97a.75.75 0 010 1.06z"/></svg>
</a>
`

// ServeAssets mounts the raw embedded /webui/static/* files under the
// given prefix. Optional: most apps will use RenderPage to inline the
// assets, but exposing them as files lets the browser cache them and
// makes devtools/source-maps work in development.
func ServeAssets(mux *http.ServeMux, prefix string) {
	mux.Handle(prefix+"/", http.StripPrefix(prefix+"/", http.FileServer(http.FS(staticFS))))
}

// WriteHTML is a small convenience for handlers: sets the Content-Type
// header and writes the rendered HTML body in one call.
func WriteHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, body)
}
