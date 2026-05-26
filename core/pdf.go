package core

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// PDFBranding is the branding line shown in PDF exports.
// Override this in an init() function to customize.
var PDFBranding = "Gohort"

// PDF layout constants. Tuned to match the browser export's CSS:
//
//	body { line-height: 1.7; color: #24292f; }
//	h2   { color: #0550ae; border-bottom: 1px solid #d0d7de; }
const (
	pdfPageW      = 210.0 // A4 width in mm
	pdfPageH      = 297.0 // A4 height in mm
	pdfMarginL    = 20.0
	pdfMarginR    = 20.0
	pdfMarginT    = 20.0
	pdfMarginB    = 20.0
	pdfBodyW      = pdfPageW - pdfMarginL - pdfMarginR
	pdfLineH      = 7.0  // ~1.7x line height for 12pt body text
	pdfParaGap    = 3.0  // gap after paragraphs
	pdfSectionGap = 6.0  // gap before section headers
	pdfListIndent = 6.0  // bullet/number indent
	pdfQuoteInset = 8.0  // blockquote left inset
)

// pdfWriter wraps an fpdf.Fpdf instance and provides markdown-aware
// rendering helpers. All coordinates are in millimeters.
type pdfWriter struct {
	pdf       *fpdf.Fpdf
	bodyW     float64
	curStyle  string  // current base font style ("", "B", "I", "BI")
	fontScale float64 // multiplier for all setFont sizes; 1.0 = default
	// curR/G/B mirror the most recently requested text color. The
	// footer callback consults these to restore the body color
	// after rendering its own gray "Page N" text — without this,
	// mid-paragraph page breaks bled gray into the next page's
	// content (visible as headings flipping to black mid-flow).
	curR, curG, curB int
}

// sanitizeUTF8 replaces smart quotes, em-dashes, and other Unicode
// characters that fpdf's built-in Latin-1 fonts cannot render.
func sanitizeUTF8(s string) string {
	r := strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", // smart single quotes
		"\u201C", "\"", "\u201D", "\"", // smart double quotes
		"\u2013", "-", "\u2014", "-", // en-dash, em-dash
		"\u2010", "-", "\u2011", "-", // hyphen, non-breaking hyphen
		"\u2012", "-", // figure dash
		"\u2026", "...", // ellipsis
		"\u00A0", " ", // non-breaking space
		"\u2022", "-", // bullet
		"\u2032", "'", "\u2033", "\"", // prime, double prime
		"\u00AB", "\"", "\u00BB", "\"", // guillemets
		"\u2039", "'", "\u203A", "'", // single guillemets
		"\u201A", ",", "\u201E", "\"", // low quotes
		"\u2002", " ", "\u2003", " ", // en space, em space
		"\u2009", " ", "\u200A", " ", // thin space, hair space
		"\u200B", "", // zero-width space
		"\u2060", "", // word joiner
		"\uFEFF", "", // BOM
		"\u00E2\u0080\u0099", "'", // mojibake apostrophe
		"\u00E2\u0080\u0093", "-", // mojibake en-dash
		"\u00E2\u0080\u0094", "-", // mojibake em-dash
		"\u00E2\u0080\u0098", "'", // mojibake left single quote
		"\u00E2\u0080\u009C", "\"", // mojibake left double quote
		"\u00E2\u0080\u009D", "\"", // mojibake right double quote
		"\u00E2\u0080\u0091", "-", // mojibake non-breaking hyphen
		"\u00E2\u0080\u0090", "-", // mojibake hyphen
		"\u00E2\u0080\u00A6", "...", // mojibake ellipsis
		"\u00E2\u0084\u00A2", "(TM)", // mojibake trademark
		"\u00E2\u0080\u00A2", "-", // mojibake bullet
	)
	return r.Replace(s)
}

// formatDate converts an RFC3339 or date string to a readable format.
func formatPDFDate(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("January 2, 2006")
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("January 2, 2006")
	}
	return s
}

// MarkdownToPDF renders markdown text into a PDF document and writes
// the result to w. title is printed as a cover heading on the first page.
// date is printed below the title (pass "" to omit).
func MarkdownToPDF(w io.Writer, title, date, markdown string) error {
	return MarkdownToPDFScaled(w, title, date, markdown, 1.0)
}

// MarkdownToPDFScaled renders markdown to PDF with all font sizes
// multiplied by scale. scale=1.0 is the default look (12pt body);
// scale=0.85 (~10pt body) reads tighter and fits more on a page,
// useful for transcript-style exports with many section headers.
// Layout, colors, and structure are unchanged — only sizes scale.
func MarkdownToPDFScaled(w io.Writer, title, date, markdown string, scale float64) error {
	if scale <= 0 {
		scale = 1.0
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pdfMarginL, pdfMarginT, pdfMarginR)
	pdf.SetAutoPageBreak(true, pdfMarginB)
	pdf.AddPage()

	pw := &pdfWriter{pdf: pdf, bodyW: pdfBodyW, fontScale: scale}
	// Default body color so the first paragraph renders correctly
	// after the footer fires on the very first page.
	pw.curR, pw.curG, pw.curB = 36, 41, 47

	// Page numbers in footer. The footer fires on every page break;
	// auto-pagebreak triggers it mid-paragraph, and we have to
	// restore the body's text color after — otherwise the first
	// chunk of text on the new page renders in the footer's gray.
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Arial", "", 8)
		pdf.SetTextColor(160, 160, 160)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d", pdf.PageNo()), "", 0, "C", false, 0, "")
		// Restore the body's color so post-break content is correct.
		pdf.SetTextColor(pw.curR, pw.curG, pw.curB)
	})

	title = sanitizeUTF8(title)
	date = formatPDFDate(date)

	// Title: black bold, matching the browser export header.
	pw.setFont("Arial", "B", 18)
	pw.setColor(24, 24, 24)
	pw.writeBlock(title, "L")
	pw.ln(1)
	// Branding and date line.
	pw.setFont("Arial", "", 10)
	pw.setColor(102, 102, 102) // #666
	dateLine := PDFBranding
	if date != "" {
		dateLine = date + "  |  " + dateLine
	}
	pw.writeBlock(dateLine, "L")
	pw.ln(2)
	// Black divider line under title.
	y := pdf.GetY()
	pdf.SetDrawColor(24, 24, 24)
	pdf.SetLineWidth(0.5)
	pdf.Line(pdfMarginL, y, pdfPageW-pdfMarginR, y)
	pw.ln(pdfSectionGap)

	// Render body.
	pw.renderMarkdown(sanitizeUTF8(markdown))

	return pdf.Output(w)
}

// renderMarkdown walks markdown line-by-line and emits PDF elements.
func (pw *pdfWriter) renderMarkdown(md string) {
	lines := strings.Split(md, "\n")
	in_code := false
	in_list := false
	in_ol := false
	in_sources := false
	ol_num := 0
	// Side-color boxes — `:::for`, `:::against`, `:::verdict`,
	// `:::accent` open a styled box; `:::` closes it. Used by the
	// debate transcript exporter to give FOR/AGAINST args a left
	// border + tinted fill that mirrors the web UI's argument cards.
	in_box := false
	boxKind := ""
	boxLines := []string{}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmedLine := strings.TrimSpace(line)

		// Side-color box markers. `:::name` opens, `:::` (or `:::end`)
		// closes. Recognized kinds: "for", "against", "verdict",
		// "accent". Other names render as a generic accent box.
		if strings.HasPrefix(trimmedLine, ":::") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmedLine, ":::"))
			if in_box && (rest == "" || rest == "end") {
				pw.renderColorBox(boxKind, boxLines)
				in_box = false
				boxKind = ""
				boxLines = nil
				continue
			}
			if !in_box && rest != "" {
				in_box = true
				boxKind = rest
				boxLines = nil
				continue
			}
			// Mismatched marker — fall through and render as normal text.
		}
		if in_box {
			boxLines = append(boxLines, line)
			continue
		}

		// Code blocks.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in_code {
				in_code = false
				pw.ln(pdfParaGap)
			} else {
				if in_list { in_list = false }
				if in_ol { in_ol = false }
				in_code = true
				pw.ln(1)
			}
			continue
		}
		if in_code {
			pw.setFont("Courier", "", 9)
			pw.setColor(50, 50, 50)
			pw.pdf.SetFillColor(245, 245, 245)
			pw.pdf.SetX(pdfMarginL + 2)
			pw.pdf.MultiCell(pw.bodyW-4, 4.5, line, "", "L", true)
			continue
		}

		stripped := strings.TrimSpace(line)

		// Blank lines. Every non-blank paragraph already emits its
		// own trailing pdfParaGap, so a blank line between two
		// paragraphs would otherwise stack two gaps and read as
		// double-spacing. Treat blank lines as a soft no-op (just
		// reset list state); paragraph-internal pdfParaGap handles
		// inter-line spacing.
		if stripped == "" {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			if in_sources {
				pw.ln(0.8)
			}
			continue
		}

		// Detect sources section for tighter formatting.
		if strings.HasPrefix(stripped, "## Sources") {
			in_sources = true
		}

		// H1.
		if strings.HasPrefix(stripped, "# ") && !strings.HasPrefix(stripped, "## ") {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			pw.ln(pdfSectionGap)
			pw.setFont("Arial", "B", 18)
			pw.setColor(36, 41, 47) // #24292f
			pw.writeInline(stripped[2:])
			pw.ln(pdfParaGap)
			continue
		}

		// H2: browser default ~18pt, color #0550ae with bottom border.
		if strings.HasPrefix(stripped, "## ") {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			pw.ln(pdfSectionGap + 2)
			pw.setFont("Arial", "B", 18)
			pw.setColor(5, 80, 174) // #0550ae
			pw.writeInline(stripped[3:])
			// Bottom border like CSS border-bottom: 1px solid #d0d7de.
			y := pw.pdf.GetY()
			pw.pdf.SetDrawColor(208, 215, 222) // #d0d7de
			pw.pdf.SetLineWidth(0.3)
			pw.pdf.Line(pdfMarginL, y+1, pdfPageW-pdfMarginR, y+1)
			pw.ln(pdfParaGap + 2)
			continue
		}

		// H3: browser default ~14pt, color #24292f.
		if strings.HasPrefix(stripped, "### ") {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			pw.ln(pdfSectionGap)
			pw.setFont("Arial", "B", 14)
			pw.setColor(36, 41, 47) // #24292f
			pw.writeInline(stripped[4:])
			pw.ln(pdfParaGap)
			continue
		}

		// H4.
		if strings.HasPrefix(stripped, "#### ") {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			pw.ln(pdfSectionGap - 1)
			pw.setFont("Arial", "B", 12)
			pw.setColor(36, 41, 47)
			pw.writeInline(stripped[5:])
			pw.ln(pdfParaGap)
			continue
		}

		// Source index lines: [N] ... - render URL as clickable link.
		if in_sources && len(stripped) > 2 && stripped[0] == '[' {
			pw.writeSourceLine(stripped)
			pw.ln(0.8)
			continue
		}

		// Bullet list: browser uses margin 0.5rem top, li margin-bottom 0.3rem.
		if strings.HasPrefix(stripped, "- ") || strings.HasPrefix(stripped, "* ") {
			if in_ol { in_ol = false; ol_num = 0 }
			in_list = true
			pw.setFont("Arial", "", 12)
			pw.setColor(36, 41, 47)
			pw.pdf.SetX(pdfMarginL + pdfListIndent)
			pw.pdf.Write(pdfLineH, "-  ")
			pw.writeInlineAt(stripped[2:], pw.bodyW-pdfListIndent-4)
			pw.ln(1.5)
			continue
		}

		// Numbered list.
		if len(stripped) > 2 && stripped[0] >= '0' && stripped[0] <= '9' && strings.Contains(stripped[:min(5, len(stripped))], ". ") {
			if in_list { in_list = false }
			in_ol = true
			ol_num++
			dot := strings.Index(stripped, ". ")
			pw.setFont("Arial", "", 12)
			pw.setColor(36, 41, 47)
			pw.pdf.SetX(pdfMarginL + pdfListIndent)
			pw.pdf.Write(pdfLineH, fmt.Sprintf("%d. ", ol_num))
			pw.writeInlineAt(stripped[dot+2:], pw.bodyW-pdfListIndent-6)
			pw.ln(1.5)
			continue
		}

		// Blockquote.
		if strings.HasPrefix(stripped, "> ") {
			if in_list { in_list = false }
			if in_ol { in_ol = false; ol_num = 0 }
			y := pw.pdf.GetY()
			pw.pdf.SetDrawColor(5, 80, 174)
			pw.pdf.SetLineWidth(0.6)
			pw.setFont("Arial", "I", 12)
			pw.setColor(80, 80, 80)
			pw.pdf.SetX(pdfMarginL + pdfQuoteInset)
			pw.pdf.MultiCell(pw.bodyW-pdfQuoteInset, pdfLineH, sanitizeUTF8(stripped[2:]), "", "L", false)
			y2 := pw.pdf.GetY()
			pw.pdf.Line(pdfMarginL+pdfQuoteInset-3, y, pdfMarginL+pdfQuoteInset-3, y2)
			pw.ln(pdfParaGap)
			continue
		}

		// Horizontal rule.
		if stripped == "---" || stripped == "***" || stripped == "___" {
			pw.ln(3)
			y := pw.pdf.GetY()
			pw.pdf.SetDrawColor(208, 215, 222)
			pw.pdf.SetLineWidth(0.3)
			pw.pdf.Line(pdfMarginL, y, pdfPageW-pdfMarginR, y)
			pw.ln(4)
			continue
		}

		// Regular paragraph: 12pt, #24292f, 1.7x line height.
		if in_list { in_list = false }
		if in_ol { in_ol = false; ol_num = 0 }
		pw.setFont("Arial", "", 12)
		pw.setColor(36, 41, 47) // #24292f
		pw.writeInline(stripped)
		pw.ln(pdfParaGap)
	}
}

// inlineSpan represents a styled segment of text within a line.
type inlineSpan struct {
	text  string
	style string // "" = normal, "B" = bold, "I" = italic
}

// Bold/italic regex (order matters: bold before italic).
var (
	pdfBoldRE   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	pdfItalicRE = regexp.MustCompile(`\*(.+?)\*`)
	pdfLinkRE   = regexp.MustCompile(`\[((?:[^\[\]]|\[[^\]]*\])*)\]\(((?:[^\(\)]|\([^\)]*\))+)\)`)
)

// parseInline splits a markdown line into styled spans.
func parseInline(text string) []inlineSpan {
	// Collapse markdown links to just the text.
	text = pdfLinkRE.ReplaceAllString(text, "$1")

	var spans []inlineSpan
	for len(text) > 0 {
		bold_loc := pdfBoldRE.FindStringIndex(text)
		italic_loc := pdfItalicRE.FindStringIndex(text)

		var loc []int
		var style string
		var re *regexp.Regexp
		if bold_loc != nil && (italic_loc == nil || bold_loc[0] <= italic_loc[0]) {
			loc = bold_loc
			style = "B"
			re = pdfBoldRE
		} else if italic_loc != nil {
			loc = italic_loc
			style = "I"
			re = pdfItalicRE
		}

		if loc == nil {
			spans = append(spans, inlineSpan{text: text, style: ""})
			break
		}

		if loc[0] > 0 {
			spans = append(spans, inlineSpan{text: text[:loc[0]], style: ""})
		}
		match := re.FindStringSubmatch(text[loc[0]:loc[1]])
		if match != nil {
			spans = append(spans, inlineSpan{text: match[1], style: style})
		}
		text = text[loc[1]:]
	}
	return spans
}

// writeInline renders a markdown line with inline bold/italic at full body width.
func (pw *pdfWriter) writeInline(text string) {
	pw.writeInlineAt(text, pw.bodyW)
}

// writeInlineAt renders inline markdown within the given width from the
// current X position. The caller's current font style is used as the base
// for unstyled spans, so headers set to bold retain their weight.
func (pw *pdfWriter) writeInlineAt(text string, width float64) {
	spans := parseInline(text)
	size, _ := pw.pdf.GetFontSize()

	for _, sp := range spans {
		style := sp.style
		if style == "" {
			style = pw.curStyle
		} else if strings.Contains(pw.curStyle, "B") && !strings.Contains(style, "B") {
			style = "B" + style
		}
		pw.pdf.SetFont("Arial", style, size)
		pw.pdf.Write(pdfLineH, sp.text)
	}
	pw.pdf.SetFont("Arial", pw.curStyle, size)
	pw.pdf.Ln(pdfLineH)
}

// writeSourceLine renders a source citation line with the URL as a clickable link.
// Format: [N] [tag] Title - domain - https://url
func (pw *pdfWriter) writeSourceLine(line string) {
	pw.setFont("Arial", "", 10)

	// Find the URL in the line.
	url_idx := strings.Index(line, "https://")
	if url_idx < 0 {
		url_idx = strings.Index(line, "http://")
	}

	if url_idx < 0 {
		// No URL found, render as plain text.
		pw.setColor(80, 80, 80)
		pw.pdf.MultiCell(pw.bodyW, 4.5, line, "", "L", false)
		return
	}

	// Text before URL.
	prefix := strings.TrimRight(line[:url_idx], " -")
	url := strings.TrimSpace(line[url_idx:])

	pw.setColor(80, 80, 80)
	pw.pdf.Write(4.5, prefix+" - ")

	// URL as clickable blue link.
	pw.setColor(5, 80, 174)
	pw.pdf.WriteLinkString(4.5, url, url)

	pw.setColor(80, 80, 80)
	pw.pdf.Ln(4.5)
}

// writeBlock writes a simple text block using MultiCell (auto-wrap).
func (pw *pdfWriter) writeBlock(text, align string) {
	pw.pdf.MultiCell(pw.bodyW, pdfLineH, text, "", align, false)
}

// ln adds vertical spacing.
func (pw *pdfWriter) ln(h float64) {
	pw.pdf.Ln(h)
}

// setFont sets the current font and tracks the base style. When
// pw.fontScale is non-zero (default 1.0), every requested size is
// multiplied by the scale — this is how MarkdownToPDFScaled tightens
// the document for transcripts that pack a lot of section headers.
func (pw *pdfWriter) setFont(family, style string, size float64) {
	pw.curStyle = style
	scale := pw.fontScale
	if scale == 0 {
		scale = 1.0
	}
	pw.pdf.SetFont(family, style, size*scale)
}

// setColor sets the text color and remembers it so the footer
// callback (which sets a gray for the page-number footer) can
// restore the body color after auto-pagebreak fires mid-render.
func (pw *pdfWriter) setColor(r, g, b int) {
	pw.curR, pw.curG, pw.curB = r, g, b
	pw.pdf.SetTextColor(r, g, b)
}

// renderColorBox draws a side-color callout: a thin colored vertical
// stripe on the left, content rendered normally inside with a small
// indent. No fill — keeps the document looking clean and printable
// while still distinguishing FOR / AGAINST / verdict sections by
// edge color. Handles page breaks: if content spans multiple pages,
// the stripe is drawn segment-by-segment on each page so no portion
// of the box is missing the colored edge.
func (pw *pdfWriter) renderColorBox(kind string, lines []string) {
	var r, g, bl int
	switch strings.ToLower(kind) {
	case "for":
		r, g, bl = 63, 185, 80 // green
	case "against":
		r, g, bl = 248, 81, 73 // red
	case "verdict":
		r, g, bl = 88, 166, 255 // accent blue
	default:
		r, g, bl = 128, 128, 128
	}

	const stripeW = 1.2 // mm — slightly wider than 1mm so it prints visibly
	const indent = 6.0  // text indent past stripe (was 3, too tight on verdict)
	const padTop = 1.0
	const padBot = 1.0

	pw.ln(0.5) // tiny visual breathing room before the stripe starts

	startY := pw.pdf.GetY()
	startPage := pw.pdf.PageNo()
	prevBodyW := pw.bodyW
	pw.bodyW = prevBodyW - stripeW - indent
	innerX := pdfMarginL + stripeW + indent
	// Critical: shift the page's LEFT margin so that wrapped lines
	// indent past the stripe instead of jumping back to pdfMarginL
	// (where the stripe lives). pdf.Write wraps to the current page
	// left-margin, not whatever width we'd like — so we have to push
	// the margin temporarily and restore it after the box.
	pw.pdf.SetLeftMargin(innerX)
	defer pw.pdf.SetLeftMargin(pdfMarginL)

	// Render content. fpdf auto-paginates inside MultiCell; we
	// detect page transitions and capture per-page Y ranges so the
	// stripe can be drawn on each page after content lands.
	type pageRange struct {
		page int
		y1   float64
		y2   float64
	}
	currentRange := pageRange{page: startPage, y1: startY}
	pageRanges := []pageRange{}

	for _, l := range lines {
		stripped := strings.TrimSpace(l)
		if stripped == "" {
			continue
		}
		// Snapshot page before render — if it changes, content moved
		// to a new page. Close out the previous range and start a new
		// one at the new page's top margin.
		beforePage := pw.pdf.PageNo()
		pw.pdf.SetX(innerX)
		// Headings inside boxes stay full size — the box is just a
		// side-color marker, not a smaller "card" zone.
		if strings.HasPrefix(stripped, "### ") {
			pw.setFont("Arial", "B", 14)
			pw.setColor(r, g, bl)
			pw.writeInlineAt(stripped[4:], pw.bodyW)
			pw.ln(pdfParaGap)
		} else if strings.HasPrefix(stripped, "## ") {
			pw.setFont("Arial", "B", 16)
			pw.setColor(r, g, bl)
			pw.writeInlineAt(stripped[3:], pw.bodyW)
			pw.ln(pdfParaGap)
		} else if strings.HasPrefix(stripped, "- ") || strings.HasPrefix(stripped, "* ") {
			pw.setFont("Arial", "", 12)
			pw.setColor(36, 41, 47)
			pw.pdf.SetX(innerX + 4)
			pw.pdf.Write(pdfLineH, "-  ")
			pw.writeInlineAt(stripped[2:], pw.bodyW-6)
			pw.ln(1.5)
		} else {
			pw.setFont("Arial", "", 12)
			pw.setColor(36, 41, 47)
			pw.writeInlineAt(stripped, pw.bodyW)
			pw.ln(pdfParaGap)
		}
		afterPage := pw.pdf.PageNo()
		if afterPage != beforePage {
			// Content paged. Close out previous range at the bottom
			// of the previous page, open a new range at the top of
			// the new page.
			currentRange.y2 = pdfPageH - pdfMarginB
			pageRanges = append(pageRanges, currentRange)
			currentRange = pageRange{page: afterPage, y1: pdfMarginT}
		}
	}
	currentRange.y2 = pw.pdf.GetY()
	pageRanges = append(pageRanges, currentRange)

	endPage := pw.pdf.PageNo()
	pw.bodyW = prevBodyW

	// Draw stripe segments per page. Switch pages with SetPage so
	// each Rect call lands on the right one; restore current page
	// at the end so subsequent content keeps appending where it
	// left off.
	pw.pdf.SetFillColor(r, g, bl)
	pw.pdf.SetDrawColor(r, g, bl)
	for _, pr := range pageRanges {
		if pr.y2 <= pr.y1 {
			continue
		}
		pw.pdf.SetPage(pr.page)
		pw.pdf.Rect(pdfMarginL, pr.y1-padTop, stripeW, (pr.y2-pr.y1)+padTop+padBot, "F")
	}
	pw.pdf.SetPage(endPage)

	pw.ln(pdfParaGap)
}

// MarkdownToPDFBytes is a convenience wrapper that returns the PDF as bytes.
func MarkdownToPDFBytes(title, date, markdown string) ([]byte, error) {
	var buf bytes.Buffer
	if err := MarkdownToPDF(&buf, title, date, markdown); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
