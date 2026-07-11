// HTML→PDF seam. the media package defines the capability; the browser tool
// package (tools/browser) registers a go-rod / headless-Chromium implementation
// that print-to-PDFs an HTML document. Consumers (e.g. the guides app) render
// their HTML and call HTMLToPDF to get a PDF that matches the on-screen HTML
// exactly — styling, tables, clickable internal links — instead of a separate,
// weaker PDF renderer. Falls back is the CALLER's job: HTMLToPDF returns an error
// when the browser isn't registered or Chromium can't launch, and the caller can
// drop to the pure-Go MarkdownToPDF path.
package media

import "errors"

var htmlToPDFFn func(html []byte) ([]byte, error)

// RegisterHTMLToPDF installs the HTML→PDF implementation. Called once at startup
// from tools/browser.
func RegisterHTMLToPDF(fn func(html []byte) ([]byte, error)) { htmlToPDFFn = fn }

// HTMLToPDFAvailable reports whether an implementation is registered. A true
// result doesn't guarantee success (Chromium may still fail to launch) — it just
// means the path exists; callers should still handle HTMLToPDF errors.
func HTMLToPDFAvailable() bool { return htmlToPDFFn != nil }

// HTMLToPDF renders a full HTML document to PDF via the registered headless
// browser. Returns an error when no implementation is registered or the render
// fails; callers fall back to MarkdownToPDF.
func HTMLToPDF(html []byte) ([]byte, error) {
	if htmlToPDFFn == nil {
		return nil, errors.New("html-to-pdf is not available (headless browser not registered)")
	}
	return htmlToPDFFn(html)
}
