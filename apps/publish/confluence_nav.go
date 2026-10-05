package publish

// The document's navigation within itself, rebuilt in Confluence storage
// format for the API route (docs.DocNav says what it is).
//
// The converter writes an in-page link as <a href="#install">, gohort's anchor,
// and Confluence names its heading anchors its own way, so every such link and
// every table-of-contents entry pointed at nothing. Here, after conversion:
//
//   - a heading a link points at gets an Anchor macro carrying its gohort
//     anchor, and the link becomes an ac:link to that anchor: Confluence's
//     own way to link within a page, independent of how it names headings;
//   - a link that points at no heading keeps its words and loses the link;
//   - the table of contents (its heading and the list under it) becomes
//     Confluence's Table of Contents macro, which lists the page's real
//     headings and stays right when the page is edited there.

import (
	"html"
	"regexp"
	"strings"

	"github.com/cmcoffee/gohort/core/docs"
)

var (
	confHeadingRe = regexp.MustCompile(`(?s)<(h[1-6])>(.*?)</h[1-6]>`)
	confAnchorRe  = regexp.MustCompile(`(?s)<a href="#([^"]*)">(.*?)</a>`)
	confTagRe     = regexp.MustCompile(`<[^>]+>`)
)

const confTOCMacro = `<ac:structured-macro ac:name="toc" ac:schema-version="1" />`

func confAnchorMacro(name string) string {
	return `<ac:structured-macro ac:name="anchor" ac:schema-version="1"><ac:parameter ac:name="">` + html.EscapeString(name) + `</ac:parameter></ac:structured-macro>`
}

// confPlain is an element's text: tags dropped, entities read.
func confPlain(s string) string {
	return strings.TrimSpace(html.UnescapeString(confTagRe.ReplaceAllString(s, "")))
}

// confluenceNav rebuilds nav in storage, the converter's output for the same
// document. Nothing outside the navigation changes.
func confluenceNav(storage string, nav docs.DocNav) string {
	if nav.Empty() {
		return storage
	}
	anchorOf := map[string]string{} // heading text, lowercased -> its anchor
	for _, h := range nav.Headings {
		if _, dup := anchorOf[strings.ToLower(h.Text)]; !dup {
			anchorOf[strings.ToLower(h.Text)] = h.Anchor
		}
	}
	// What each link anchor resolves to: the anchor of the heading it means.
	target := map[string]string{}
	linked := map[string]bool{} // anchors that need an Anchor macro
	for _, l := range nav.Links {
		if l.Heading == "" {
			continue
		}
		if a, ok := anchorOf[strings.ToLower(l.Heading)]; ok {
			target[l.Anchor] = a
			linked[a] = true
		}
	}
	if nav.Contents != "" {
		storage = confReplaceContents(storage, nav.Contents)
	}
	storage = confHeadingRe.ReplaceAllStringFunc(storage, func(h string) string {
		m := confHeadingRe.FindStringSubmatch(h)
		a, ok := anchorOf[strings.ToLower(confPlain(m[2]))]
		if !ok || !linked[a] {
			return h
		}
		return "<" + m[1] + ">" + confAnchorMacro(a) + m[2] + "</" + m[1] + ">"
	})
	return confAnchorRe.ReplaceAllStringFunc(storage, func(link string) string {
		m := confAnchorRe.FindStringSubmatch(link)
		a, ok := target[html.UnescapeString(m[1])]
		if !ok {
			// Points at no heading here: keep the words, drop the dead link.
			return m[2]
		}
		return `<ac:link ac:anchor="` + html.EscapeString(a) + `"><ac:link-body>` + m[2] + `</ac:link-body></ac:link>`
	})
}

// confReplaceContents swaps the table of contents (its heading and the list
// right under it) for the Table of Contents macro.
func confReplaceContents(storage, heading string) string {
	for _, loc := range confHeadingRe.FindAllStringSubmatchIndex(storage, -1) {
		if !strings.EqualFold(confPlain(storage[loc[4]:loc[5]]), heading) {
			continue
		}
		rest := storage[loc[1]:]
		trimmed := strings.TrimLeft(rest, " \n\t")
		var tag string
		switch {
		case strings.HasPrefix(trimmed, "<ol>"):
			tag = "ol"
		case strings.HasPrefix(trimmed, "<ul>"):
			tag = "ul"
		default:
			continue
		}
		start := loc[1] + (len(rest) - len(trimmed))
		end := confListEnd(storage, start, tag)
		if end < 0 {
			continue
		}
		return storage[:loc[0]] + confTOCMacro + storage[end:]
	}
	return storage
}

// confListEnd is the index just past the </tag> closing the list that opens at
// start, counting nested lists of the same tag.
func confListEnd(s string, start int, tag string) int {
	open, closeTag := "<"+tag+">", "</"+tag+">"
	depth := 0
	for i := start; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], open):
			depth++
			i += len(open)
		case strings.HasPrefix(s[i:], closeTag):
			depth--
			i += len(closeTag)
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return -1
}
