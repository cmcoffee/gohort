// HTML→PDF via the shared headless Chromium (the same instance browse_page +
// screenshot_page use). Registers the media HTMLToPDF seam so apps render their
// HTML and get a print-to-PDF that matches it exactly — CSS, tables, and
// clickable internal/ToC links that the pure-Go fpdf renderer can't produce.
package browser

import (
	"fmt"
	"io"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterHTMLToPDF(htmlToPDF) }

// htmlToPDF loads the HTML into a fresh page and prints it to PDF. Returns an
// error (so the caller can fall back to fpdf) when Chromium isn't available.
func htmlToPDF(html []byte) ([]byte, error) {
	shared.launch()
	if shared.initErr != nil {
		return nil, shared.initErr
	}
	b := shared.browser
	if b == nil {
		return nil, fmt.Errorf("headless browser unavailable")
	}
	var out []byte
	err := rod.Try(func() {
		page := b.MustPage()
		defer page.MustClose()
		page.MustSetDocumentContent(string(html))
		// Let layout settle (fonts/styles applied) before printing.
		page.Timeout(15 * time.Second).MustWaitStable()
		// PrintBackground so CSS backgrounds (code blocks, the contents panel)
		// render; preferCSSPageSize off so the doc uses the default A4 page.
		sr, e := page.PDF(&proto.PagePrintToPDF{PrintBackground: true})
		if e != nil {
			panic(e)
		}
		data, e := io.ReadAll(sr)
		_ = sr.Close()
		if e != nil {
			panic(e)
		}
		out = data
	})
	if err != nil {
		return nil, fmt.Errorf("chromium print-to-pdf failed: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("chromium produced an empty pdf")
	}
	return out, nil
}
