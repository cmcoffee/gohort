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
	"fmt"
	"html"
	"net/http"
	"strings"
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
func BaseCSS() string { return asset("base.css") }

// BaseJS returns the shared utility JS — access-control check that
// hides Push-to-TechWriter buttons, live session ribbon polling, and
// small DOM helpers. Apps append pipeline-specific JS via PageOpts.AppJS.
func BaseJS() string { return asset("base.js") }

// PageOpts describes a single page render. Required fields: Title,
// AppName, BodyHTML. Everything else is optional.
type PageOpts struct {
	Title    string // <title> contents
	AppName  string // app label (e.g. "Research") shown in headers/ribbons
	BodyHTML string // app-specific HTML body (main panel)
	AppCSS   string // app-specific CSS appended after BaseCSS
	AppJS    string // app-specific JS appended after BaseJS
	InitJS   string // optional inline init script run on DOMContentLoaded
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
	sb.WriteString(`
</style>
</head>
<body data-app="`)
	sb.WriteString(html.EscapeString(opts.AppName))
	sb.WriteString(`">
`)
	if opts.Prefix != "" {
		sb.WriteString(backChromeHTML)
	}
	body := opts.BodyHTML
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

const backChromeHTML = `<a id="dashboard-back" href="/" title="Dashboard">
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
