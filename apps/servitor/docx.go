package servitor

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ── OOXML boilerplate ────────────────────────────────────────────────────────

const docxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`

const docxRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

const docxDocRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:docDefaults>
    <w:rPrDefault><w:rPr>
      <w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/>
      <w:sz w:val="24"/><w:szCs w:val="24"/>
    </w:rPr></w:rPrDefault>
  </w:docDefaults>
  <w:style w:type="paragraph" w:default="1" w:styleId="Normal">
    <w:name w:val="Normal"/>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/>
    <w:basedOn w:val="Normal"/><w:next w:val="Normal"/>
    <w:pPr><w:outlineLvl w:val="0"/><w:spacing w:before="240" w:after="60"/></w:pPr>
    <w:rPr><w:b/><w:bCs/><w:sz w:val="40"/><w:szCs w:val="40"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading2">
    <w:name w:val="heading 2"/>
    <w:basedOn w:val="Normal"/><w:next w:val="Normal"/>
    <w:pPr><w:outlineLvl w:val="1"/><w:spacing w:before="200" w:after="60"/></w:pPr>
    <w:rPr><w:b/><w:bCs/><w:sz w:val="32"/><w:szCs w:val="32"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading3">
    <w:name w:val="heading 3"/>
    <w:basedOn w:val="Normal"/><w:next w:val="Normal"/>
    <w:pPr><w:outlineLvl w:val="2"/><w:spacing w:before="160" w:after="40"/></w:pPr>
    <w:rPr><w:b/><w:bCs/><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading4">
    <w:name w:val="heading 4"/>
    <w:basedOn w:val="Normal"/><w:next w:val="Normal"/>
    <w:pPr><w:outlineLvl w:val="3"/><w:spacing w:before="120" w:after="40"/></w:pPr>
    <w:rPr><w:b/><w:bCs/><w:i/><w:iCs/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="ListParagraph">
    <w:name w:val="List Paragraph"/>
    <w:basedOn w:val="Normal"/>
    <w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Code">
    <w:name w:val="Code"/>
    <w:basedOn w:val="Normal"/>
    <w:pPr><w:shd w:val="clear" w:color="auto" w:fill="F5F5F5"/><w:spacing w:before="0" w:after="0"/></w:pPr>
    <w:rPr>
      <w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New"/>
      <w:sz w:val="20"/><w:szCs w:val="20"/>
    </w:rPr>
  </w:style>
</w:styles>`

// ── Inline parser ────────────────────────────────────────────────────────────

type docRun struct {
	text   string
	bold   bool
	italic bool
	code   bool
}

// parseInline converts inline markdown to a slice of styled runs.
func parseInline(s string) []docRun {
	var runs []docRun
	var cur strings.Builder
	bold, italic, inCode := false, false, false

	flush := func() {
		if cur.Len() > 0 {
			runs = append(runs, docRun{text: cur.String(), bold: bold, italic: italic})
			cur.Reset()
		}
	}
	flushCode := func() {
		if cur.Len() > 0 {
			runs = append(runs, docRun{text: cur.String(), code: true})
			cur.Reset()
		}
	}

	i := 0
	for i < len(s) {
		// Backtick code span.
		if s[i] == '`' {
			if inCode {
				flushCode()
				inCode = false
			} else {
				flush()
				inCode = true
			}
			i++
			continue
		}
		if inCode {
			cur.WriteByte(s[i])
			i++
			continue
		}
		// Bold+italic: ***
		if i+2 < len(s) && s[i] == '*' && s[i+1] == '*' && s[i+2] == '*' {
			flush()
			bold = !bold
			italic = !italic
			i += 3
			continue
		}
		// Bold: **
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			flush()
			bold = !bold
			i += 2
			continue
		}
		// Bold: __
		if i+1 < len(s) && s[i] == '_' && s[i+1] == '_' {
			flush()
			bold = !bold
			i += 2
			continue
		}
		// Italic: *
		if s[i] == '*' {
			flush()
			italic = !italic
			i++
			continue
		}
		// Italic: _
		if s[i] == '_' {
			flush()
			italic = !italic
			i++
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	flush()
	return runs
}

// ── OOXML emitters ───────────────────────────────────────────────────────────

// xmlEsc escapes XML special characters and strips codepoints that are illegal
// in XML 1.0 (control chars except tab/LF/CR, and U+FFFE/U+FFFF).
func xmlEsc(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0x09 || r == 0x0A || r == 0x0D:
			b.WriteRune(r)
		case r < 0x20:
			// strip C0 controls (illegal in XML 1.0)
		case r == 0xFFFE || r == 0xFFFF:
			// strip non-characters
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func emitRun(r docRun) string {
	if r.text == "" {
		return ""
	}
	var rpr strings.Builder
	if r.bold {
		rpr.WriteString("<w:b/><w:bCs/>")
	}
	if r.italic {
		rpr.WriteString("<w:i/><w:iCs/>")
	}
	if r.code {
		rpr.WriteString(`<w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New"/>`)
		rpr.WriteString(`<w:color w:val="C0392B"/>`)
		rpr.WriteString("<w:sz w:val=\"20\"/><w:szCs w:val=\"20\"/>")
	}
	var out strings.Builder
	out.WriteString("<w:r>")
	if rpr.Len() > 0 {
		out.WriteString("<w:rPr>")
		out.WriteString(rpr.String())
		out.WriteString("</w:rPr>")
	}
	out.WriteString(`<w:t xml:space="preserve">`)
	out.WriteString(xmlEsc(r.text))
	out.WriteString("</w:t></w:r>")
	return out.String()
}

func emitParagraph(style string, runs []docRun) string {
	var p strings.Builder
	p.WriteString("<w:p>")
	if style != "" && style != "Normal" {
		p.WriteString(`<w:pPr><w:pStyle w:val="`)
		p.WriteString(style)
		p.WriteString(`"/></w:pPr>`)
	}
	for _, r := range runs {
		p.WriteString(emitRun(r))
	}
	p.WriteString("</w:p>")
	return p.String()
}

func emitListItem(text string, ordered bool, num int) string {
	var p strings.Builder
	p.WriteString(`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/>`)
	p.WriteString(`<w:ind w:left="720" w:hanging="360"/></w:pPr>`)
	var prefix string
	if ordered {
		prefix = fmt.Sprintf("%d.\t", num)
	} else {
		prefix = "•\t"
	}
	p.WriteString(`<w:r><w:t xml:space="preserve">`)
	p.WriteString(xmlEsc(prefix))
	p.WriteString("</w:t></w:r>")
	for _, r := range parseInline(text) {
		p.WriteString(emitRun(r))
	}
	p.WriteString("</w:p>")
	return p.String()
}

func emitCodeLine(line string) string {
	var p strings.Builder
	p.WriteString(`<w:p><w:pPr><w:pStyle w:val="Code"/>`)
	p.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="F5F5F5"/>`)
	p.WriteString(`<w:spacing w:before="0" w:after="0"/></w:pPr>`)
	p.WriteString(`<w:r><w:rPr>`)
	p.WriteString(`<w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New"/>`)
	p.WriteString(`<w:sz w:val="20"/><w:szCs w:val="20"/>`)
	p.WriteString(`</w:rPr><w:t xml:space="preserve">`)
	p.WriteString(xmlEsc(line))
	p.WriteString("</w:t></w:r></w:p>")
	return p.String()
}

func emitTable(rows [][]string, hasHeader bool) string {
	if len(rows) == 0 {
		return ""
	}
	const borders = `<w:tblBorders>` +
		`<w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`<w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`<w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`<w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
		`</w:tblBorders>`
	var t strings.Builder
	t.WriteString(`<w:tbl><w:tblPr>`)
	t.WriteString(`<w:tblW w:w="0" w:type="auto"/>`)
	t.WriteString(borders)
	t.WriteString(`</w:tblPr>`)
	for ri, row := range rows {
		isHeader := hasHeader && ri == 0
		t.WriteString("<w:tr>")
		for _, cell := range row {
			t.WriteString("<w:tc>")
			if isHeader {
				t.WriteString(`<w:tcPr><w:shd w:val="clear" w:color="auto" w:fill="E8E8E8"/></w:tcPr>`)
			}
			t.WriteString("<w:p>")
			for _, r := range parseInline(strings.TrimSpace(cell)) {
				if isHeader {
					r.bold = true
				}
				t.WriteString(emitRun(r))
			}
			t.WriteString("</w:p></w:tc>")
		}
		t.WriteString("</w:tr>")
	}
	t.WriteString("</w:tbl>")
	// Tables need a following paragraph in OOXML.
	t.WriteString("<w:p/>")
	return t.String()
}

// ── Markdown parser ──────────────────────────────────────────────────────────

var orderedListRe = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)

func markdownToDocxBody(markdown string) string {
	var body strings.Builder
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")

	inFence := false
	var codeLines []string
	var tableRows [][]string
	tableHasHeader := false
	listNum := 0

	flushCode := func() {
		for _, cl := range codeLines {
			body.WriteString(emitCodeLine(cl))
		}
		codeLines = nil
	}
	flushTable := func() {
		if len(tableRows) > 0 {
			body.WriteString(emitTable(tableRows, tableHasHeader))
			tableRows = nil
			tableHasHeader = false
		}
	}
	isSepRow := func(line string) bool {
		for _, ch := range strings.ReplaceAll(strings.ReplaceAll(line, "|", ""), " ", "") {
			if ch != '-' && ch != ':' {
				return false
			}
		}
		return true
	}
	parseTableRow := func(line string) []string {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "|")
		line = strings.TrimSuffix(line, "|")
		return strings.Split(line, "|")
	}

	for _, line := range lines {
		// Code fence toggle.
		if strings.HasPrefix(line, "```") {
			if inFence {
				flushCode()
				inFence = false
			} else {
				flushTable()
				inFence = true
			}
			continue
		}
		if inFence {
			codeLines = append(codeLines, line)
			continue
		}

		trimmed := strings.TrimSpace(line)

		// Table rows.
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			if len(tableRows) > 0 && isSepRow(trimmed) {
				tableHasHeader = true
				continue
			}
			tableRows = append(tableRows, parseTableRow(trimmed))
			continue
		}
		flushTable()

		// Blank line.
		if trimmed == "" {
			listNum = 0
			body.WriteString("<w:p></w:p>")
			continue
		}

		// Horizontal rule.
		if trimmed == "---" || trimmed == "***" || trimmed == "___" || trimmed == "- - -" {
			body.WriteString(`<w:p><w:pPr><w:pBdr>` +
				`<w:bottom w:val="single" w:sz="6" w:space="1" w:color="AAAAAA"/>` +
				`</w:pBdr></w:pPr></w:p>`)
			continue
		}

		// Headings.
		switch {
		case strings.HasPrefix(line, "#### "):
			listNum = 0
			body.WriteString(emitParagraph("Heading4", parseInline(strings.TrimSpace(line[5:]))))
		case strings.HasPrefix(line, "### "):
			listNum = 0
			body.WriteString(emitParagraph("Heading3", parseInline(strings.TrimSpace(line[4:]))))
		case strings.HasPrefix(line, "## "):
			listNum = 0
			body.WriteString(emitParagraph("Heading2", parseInline(strings.TrimSpace(line[3:]))))
		case strings.HasPrefix(line, "# "):
			listNum = 0
			body.WriteString(emitParagraph("Heading1", parseInline(strings.TrimSpace(line[2:]))))
		// Unordered list.
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			listNum = 0
			body.WriteString(emitListItem(trimmed[2:], false, 0))
		// Ordered list.
		default:
			if m := orderedListRe.FindStringSubmatch(trimmed); m != nil {
				listNum++
				body.WriteString(emitListItem(m[2], true, listNum))
			} else {
				listNum = 0
				body.WriteString(emitParagraph("Normal", parseInline(line)))
			}
		}
	}
	flushCode()
	flushTable()
	return body.String()
}

// ── ZIP assembly ─────────────────────────────────────────────────────────────

func markdownToDocx(title, markdown string) ([]byte, error) {
	bodyXML := markdownToDocxBody(markdown)

	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` +
		// Document title as Heading1.
		emitParagraph("Heading1", []docRun{{text: title}}) +
		bodyXML +
		// Required section properties.
		`<w:sectPr>` +
		`<w:pgSz w:w="12240" w:h="15840"/>` +
		`<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/>` +
		`</w:sectPr>` +
		`</w:body></w:document>`

	type zipEntry struct{ name, body string }
	entries := []zipEntry{
		{"[Content_Types].xml", docxContentTypes},
		{"_rels/.rels", docxRels},
		{"word/_rels/document.xml.rels", docxDocRels},
		{"word/styles.xml", docxStyles},
		{"word/document.xml", docXML},
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── HTTP handler ─────────────────────────────────────────────────────────────

func (T *Servitor) handleWorkspaceExport(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, id)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	content := buildExportContent(ws)
	if content == "" {
		http.Error(w, "workspace has no content to export", http.StatusUnprocessableEntity)
		return
	}
	docx, err := markdownToDocx(ws.Name, content)
	if err != nil {
		http.Error(w, "export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filename := sanitizeFilename(ws.Name) + ".docx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(docx)))
	w.Write(docx)
}

// buildExportContent assembles the markdown content to export.
// Order: draft → Q&A history → reference document appendix.
func buildExportContent(ws DocWorkspace) string {
	var b strings.Builder

	if d := strings.TrimSpace(ws.Draft); d != "" {
		b.WriteString(d)
	}

	if len(ws.Entries) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString("## Q&A History\n\n")
		for _, e := range ws.Entries {
			if q := strings.TrimSpace(e.Question); q != "" {
				b.WriteString("**Q: ")
				b.WriteString(q)
				b.WriteString("**\n\n")
			}
			if a := strings.TrimSpace(e.Answer); a != "" {
				b.WriteString(a)
				b.WriteString("\n\n")
			}
		}
	}

	// Append reference documents so the export is self-contained.
	var suppParts []string
	for _, s := range ws.Supplements {
		c := strings.TrimSpace(s.Content)
		if c == "" {
			continue
		}
		var sb strings.Builder
		sb.WriteString("### ")
		sb.WriteString(s.Name)
		sb.WriteString("\n\n")
		if s.SubPrompt != "" {
			sb.WriteString("*")
			sb.WriteString(s.SubPrompt)
			sb.WriteString("*\n\n")
		}
		sb.WriteString(c)
		suppParts = append(suppParts, sb.String())
	}
	if len(suppParts) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString("## Reference Documents\n\n")
		b.WriteString(strings.Join(suppParts, "\n\n---\n\n"))
	}

	return strings.TrimSpace(b.String())
}

// sanitizeFilename strips characters that are invalid in filenames.
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.TrimSpace(b.String())
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "workspace"
	}
	return name
}
