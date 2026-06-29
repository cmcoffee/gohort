// Guide export + standalone preview. A guide exports to PDF (via core's markdown
// PDF renderer), a self-contained HTML document (shareable / printable, styled
// inline so it stands alone), or raw markdown.
package guides

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleExport serves a guide as pdf | html | md. GET ?id=&format=…
//   - pdf:  attachment download (core MarkdownToPDFBytes).
//   - html: inline (a preview that opens in the browser) — a self-contained doc.
//   - md:   attachment download of the assembled markdown.
func (T *Guides) handleExport(w http.ResponseWriter, r *http.Request, udb Database) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	g, ok := loadGuide(udb, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := sanitizeFilename(firstNonEmpty(g.Title, "guide"))
	switch format {
	case "pdf":
		pdf, err := MarkdownToPDFBytes(g.Title, g.Updated, renderGuideMarkdown(g))
		if err != nil {
			http.Error(w, "pdf render failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, name))
		_, _ = w.Write(pdf)
	case "md", "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, name))
		_, _ = w.Write([]byte(renderGuideMarkdown(g)))
	case "html", "":
		// Inline preview — opens in a browser tab, prints/saves cleanly.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(renderGuideStandaloneHTML(g)))
	default:
		http.Error(w, "unknown format — use pdf | html | md", http.StatusBadRequest)
	}
}

// renderGuideStandaloneHTML wraps the rendered guide in a self-contained HTML
// document with inline styling (concrete colors, not theme tokens) so it reads
// correctly outside gohort — shared, printed, or saved as a file.
func renderGuideStandaloneHTML(g Guide) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>` + HTMLEscape(g.Title) + `</title><style>` + standaloneCSS + `</style></head>` +
		`<body>` + renderGuideHTML(g) + `</body></html>`
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
.guide-doc-head h1 { font-size: 2.1rem; line-height: 1.2; margin: 0 0 0.3rem; color: #0b1320; }
.guide-doc-sub { font-size: 1.05rem; color: #59636e; margin: 0 0 1.6rem; }
.guide-doc-empty { color: #59636e; font-style: italic; }
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
.guide-section-body :not(pre) > code { background: #eaeef2; padding: 0.1rem 0.35rem; border-radius: 4px; font-size: 0.9em; }
.guide-section-body blockquote { border-left: 3px solid #d6dae0; margin: 0.9rem 0; padding: 0.2rem 0 0.2rem 1rem; color: #59636e; }
.guide-section-body table { border-collapse: collapse; margin: 0.9rem 0; }
.guide-section-body th, .guide-section-body td { border: 1px solid #d6dae0; padding: 0.4rem 0.7rem; text-align: left; }
.guide-section-body a { color: #0969da; }
@media print { body { background: #fff; } .guide-doc { box-shadow: none; max-width: none; } }
`
