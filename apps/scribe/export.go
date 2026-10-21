// Document export + standalone preview. A guide exports to PDF (via
// core's markdown PDF renderer), a self-contained HTML document (shareable /
// printable, styled inline so it stands alone), raw markdown, or any format
// System Hooks can write (core/flows): Word and
// OpenDocument by pandoc, and whatever an admin adds.
package scribe

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/ui"
)

// exportMenu is the Export menu: the formats Scribe renders itself, then
// every other format System Hooks can write here now, then the bundle.
func exportMenu() []ui.WorkbenchAction {
	menu := []ui.WorkbenchAction{
		{Label: "Preview (HTML)", Kind: "download", URL: "export?id={id}&format=html"},
		{Label: "HTML file", Kind: "download", URL: "export?id={id}&format=html&download=1"},
		{Label: "PDF", Kind: "download", URL: "export?id={id}&format=pdf"},
		{Label: "Markdown", Kind: "download", URL: "export?id={id}&format=md"},
	}
	for _, ext := range flows.Available() {
		menu = append(menu, ui.WorkbenchAction{Label: flows.OutLabel(ext), Kind: "download", URL: "export?id={id}&format=" + ext})
	}
	// Every section, the subtitle, attached knowledge and sources: the one
	// export that imports back as the same document.
	return append(menu, ui.WorkbenchAction{Label: "Bundle (lossless)", Kind: "client", URL: "scribe_export_bundle"})
}

// handleExport serves a guide in a format. GET ?id=&format=…
//   - pdf:  attachment download (rendered by renderGuidePDF).
//   - html: inline (a preview that opens in the browser), a self-contained doc.
//   - md:   attachment download of the assembled markdown.
//   - any other extension (docx, odt, epub...): written by System Hooks.
//
// Every one goes through flows.Export, so a conversion an admin adapted
// and preferred replaces Scribe's own renderer for its format.
func (T *Scribe) handleExport(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	g, ownerUDB, _, _, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	// An exported file stands alone, so its pictures travel inside it.
	g = inlineGuideImages(g, ownerUDB)
	name := sanitizeFilename(firstNonEmpty(g.Title, "guide"))
	brand, siteName := docBranding()
	md, html := renderGuideMarkdown(g), renderGuideStandaloneHTML(g, brand, siteName)
	doc := flows.Document{Title: g.Title, Markdown: []byte(md), HTML: []byte(html)}
	var ext string
	var builtin func() ([]byte, error)
	switch format {
	case "pdf":
		ext, builtin = "pdf", func() ([]byte, error) { return renderGuidePDF(g) }
	case "md", "markdown":
		ext, builtin = "md", func() ([]byte, error) { return []byte(md), nil }
	case "html", "":
		ext, builtin = "html", func() ([]byte, error) { return []byte(html), nil }
	default:
		if ext = flows.ParseOutFormat(format); ext == "" {
			http.Error(w, "unknown format: use pdf, html, md, or one System Hooks writes", http.StatusBadRequest)
			return
		}
	}
	res, err := flows.Export(r.Context(), doc, ext, builtin)
	if err != nil {
		http.Error(w, flows.OutLabel(ext)+" export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", flows.OutMIME(ext))
	// The HTML is a preview that opens in the browser; with download=1 it
	// comes down as a file instead (the page is the same).
	if ext != "html" || r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, name, ext))
	}
	_, _ = w.Write(res.Out)
}

// docBranding returns the deployment's configured document brand + site name
// (admin Site Settings), used in exported-document headers/footers. Brand falls
// back to the site name, then "".
func docBranding() (brand, siteName string) {
	if AuthDB == nil {
		return "", ""
	}
	db := AuthDB()
	if db == nil {
		return "", ""
	}
	brand = strings.TrimSpace(AuthGetDocBrand(db))
	siteName = strings.TrimSpace(AuthGetSiteName(db))
	if brand == "" {
		brand = siteName
	}
	return brand, siteName
}

// renderGuidePDF produces the guide PDF. Preferred path: print-to-PDF the
// standalone HTML through the headless browser, so the PDF matches the HTML
// preview exactly (CSS, tables, clickable ToC links). Falls back to the pure-Go
// fpdf markdown renderer when the browser isn't available — weaker formatting but
// always works, no dependency.
func renderGuidePDF(g Guide) ([]byte, error) {
	if HTMLToPDFAvailable() {
		brand, siteName := docBranding()
		if pdf, err := HTMLToPDF([]byte(renderGuideStandaloneHTML(g, brand, siteName))); err == nil {
			return pdf, nil
		} else {
			Log("[guides] HTML→PDF failed, falling back to fpdf: %v", err)
		}
	}
	return MarkdownToPDFBytes(g.Title, g.Updated, renderGuideMarkdown(g))
}

// renderGuideStandaloneHTML wraps the rendered guide in a self-contained HTML
// document with inline styling (concrete colors, not theme tokens) so it reads
// correctly outside gohort — shared, printed, or saved as a file. brand renders
// as a small header label above the title; siteName + date as a footer.
func renderGuideStandaloneHTML(g Guide, brand, siteName string) string {
	head := ""
	if brand != "" {
		head = `<div class="guide-brand">` + HTMLEscape(brand) + `</div>`
	}
	footParts := []string{}
	if siteName != "" {
		footParts = append(footParts, HTMLEscape(siteName))
	}
	if d := formatExportDate(g.Updated); d != "" {
		footParts = append(footParts, d)
	}
	foot := ""
	if len(footParts) > 0 {
		foot = `<footer class="guide-foot">` + strings.Join(footParts, " - ") + `</footer>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>` + HTMLEscape(g.Title) + `</title><style>` + standaloneCSS + `</style></head>` +
		`<body><div class="guide-doc">` + head + extractDocInner(renderGuideHTML(g, false)) + foot + `</div></body></html>`
}

// extractDocInner strips the outer <article class="guide-doc">…</article> wrapper
// from renderGuideHTML so the standalone export can wrap the content in its own
// .guide-doc div (with brand header + footer) without nesting two.
func extractDocInner(html string) string {
	const open = `<article class="guide-doc"`
	const close = `</article>`
	if i := strings.Index(html, open); i >= 0 {
		html = html[i+len(open):]
		// Past the rest of the opening tag (an article carries a second class
		// and a data attribute there).
		if j := strings.Index(html, ">"); j >= 0 {
			html = html[j+1:]
		}
		if j := strings.LastIndex(html, close); j >= 0 {
			html = html[:j]
		}
	}
	return html
}

// formatExportDate renders an RFC3339 timestamp as "Jan 2, 2006", or "" on parse
// failure / empty.
func formatExportDate(s string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.Format("Jan 2, 2006")
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "guide"
	}
	return out
}

// standaloneCSS styles an exported guide as a clean printable document (light,
// serif-ish body, numbered sections, a contents block). Concrete colors so it's
// self-contained.
const standaloneCSS = `
* { box-sizing: border-box; }
body { margin: 0; background: #f6f7f9; color: #1f2328; font: 16px/1.65 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
.guide-doc { max-width: 760px; margin: 0 auto; padding: 3rem 1.5rem 5rem; background: #fff; min-height: 100vh; }
.guide-brand { font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.08em; font-weight: 700; color: #0969da; margin-bottom: 0.8rem; }
.guide-foot { margin-top: 3rem; padding-top: 1rem; border-top: 1px solid #d6dae0; font-size: 0.8rem; color: #8893a0; }
.guide-doc-head h1 { font-size: 2.1rem; line-height: 1.2; margin: 0 0 0.3rem; color: #0b1320; }
.guide-doc-sub { font-size: 1.05rem; color: #59636e; margin: 0 0 1.6rem; }
.guide-doc-empty { color: #59636e; font-style: italic; }
.guide-doc-image { display: block; width: 100%; max-height: 320px; object-fit: cover; border-radius: 8px; margin: 0 0 1.5rem; }
.guide-toc { background: #f0f2f5; border: 1px solid #d6dae0; border-radius: 10px; padding: 1rem 1.2rem; margin: 0 0 2.4rem; }
.guide-toc-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: #59636e; margin-bottom: 0.5rem; }
.guide-toc ol { margin: 0; padding-left: 1.4rem; }
.guide-toc li { margin: 0.2rem 0; }
.guide-toc a { color: #0969da; text-decoration: none; }
.guide-section { margin: 0 0 2.4rem; }
.guide-section > h2 { font-size: 1.5rem; color: #0b1320; border-bottom: 1px solid #d6dae0; padding-bottom: 0.3rem; margin: 0 0 1rem; }
.guide-section-num { color: #8893a0; font-weight: 600; margin-right: 0.3rem; }
.guide-section-body h3 { font-size: 1.18rem; color: #0b1320; margin: 1.4rem 0 0.5rem; }
.guide-section-body h4 { font-size: 1.02rem; color: #0b1320; margin: 1.1rem 0 0.4rem; }
.guide-section-body h5, .guide-section-body h6 { font-size: 0.92rem; color: #30363d; margin: 1rem 0 0.35rem; }
.guide-section-body pre { background: #0d1117; color: #e6edf3; border-radius: 8px; padding: 0.9rem 1.1rem; overflow-x: auto; font-size: 0.86rem; }
.guide-section-body img { display: block; max-width: 100%; height: auto; margin: 0.9rem 0; border: 1px solid #d6dae0; border-radius: 8px; }
.guide-section-body :not(pre) > code { background: #eaeef2; padding: 0.1rem 0.35rem; border-radius: 4px; font-size: 0.9em; }
.guide-section-body blockquote { border-left: 3px solid #d6dae0; margin: 0.9rem 0; padding: 0.2rem 0 0.2rem 1rem; color: #59636e; }
.guide-section-body table { border-collapse: collapse; margin: 0.9rem 0; }
.guide-section-body th, .guide-section-body td { border: 1px solid #d6dae0; padding: 0.4rem 0.7rem; text-align: left; }
.guide-section-body a { color: #0969da; }
@media print { body { background: #fff; } .guide-doc { box-shadow: none; max-width: none; } }
`
