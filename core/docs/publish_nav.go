package docs

// A published document's navigation within itself: its table of contents and
// the links it makes to its own sections.
//
// In oddjob those are anchors oddjob's renderer gives each heading. Published
// somewhere else they point at nothing: Confluence names its heading anchors
// its own way, and a destination reached through an MCP server or an agent
// converts the markdown with whatever it has. So the links went out as written
// and broke, and the Publisher, told the document is the author's, left them.
//
// Navigation is presentation, and presentation belongs to where the document
// is published. PublishDocument records it on every request (Nav), and each
// destination rebuilds it its own way: Confluence's API route in code, every
// destination a model publishes to (an agent, an API or MCP integration) from
// the paragraph NavInstruction writes. Nothing here changes what the document
// says.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/cmcoffee/oddjob/core/textutil"
)

// DocNav is a document's navigation within itself.
type DocNav struct {
	// Headings are the document's headings in order, with the anchors
	// oddjob's renderer gives them.
	Headings []NavHeading `json:"headings,omitempty"`
	// Links are the links the document makes to its own sections.
	Links []NavLink `json:"links,omitempty"`
	// Contents is the heading of the document's own table of contents
	// ("Contents"), or "" when it has none.
	Contents string `json:"contents,omitempty"`
}

// NavHeading is one heading and its anchor.
type NavHeading struct {
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
}

// NavLink is one link to a section of the same document. Heading is the text
// of the heading it points at, or "" when its anchor matches no heading.
type NavLink struct {
	Text    string `json:"text"`
	Anchor  string `json:"anchor"`
	Heading string `json:"heading,omitempty"`
}

// Empty reports whether there is nothing a destination has to rebuild.
func (n DocNav) Empty() bool { return len(n.Links) == 0 && n.Contents == "" }

var (
	navLinkRe     = regexp.MustCompile(`\[([^\]\n]+)\]\(#([^)\s]*)\)`)
	navHeadingRe  = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	navListItemRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
)

// contentsHeadings are the headings a table of contents goes under.
var contentsHeadings = map[string]bool{"contents": true, "table of contents": true, "toc": true, "on this page": true, "in this guide": true}

// AnalyzeNav reads md's navigation within itself: its headings, the links it
// makes to them, and the heading of its table of contents, if it has one (a
// heading such as "Contents" followed by a list).
func AnalyzeNav(md string) DocNav {
	var n DocNav
	byAnchor := map[string]string{}
	byText := map[string]string{}
	for _, h := range textutil.HeadingSlugs(md) {
		n.Headings = append(n.Headings, NavHeading{Text: h.Text, Anchor: h.Slug})
		byAnchor[h.Slug] = h.Text
		byText[strings.ToLower(h.Text)] = h.Text
	}
	inCode := false
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if m := navHeadingRe.FindStringSubmatch(t); m != nil && n.Contents == "" {
			title := strings.TrimSpace(m[1])
			if contentsHeadings[strings.ToLower(strings.Trim(title, " :"))] && nextIsList(lines[i+1:]) {
				n.Contents = title
			}
		}
		for _, m := range navLinkRe.FindAllStringSubmatch(line, -1) {
			text, anchor := strings.TrimSpace(m[1]), m[2]
			heading := byAnchor[anchor]
			if heading == "" {
				// The link text is the trusted signal (NormalizeHeadingLinks):
				// a model copies heading text reliably and drifts on slugs.
				heading = byText[strings.ToLower(text)]
			}
			n.Links = append(n.Links, NavLink{Text: text, Anchor: anchor, Heading: heading})
		}
	}
	return n
}

// nextIsList reports whether the first non-blank line in rest is a list item.
func nextIsList(rest []string) bool {
	for _, l := range rest {
		if strings.TrimSpace(l) == "" {
			continue
		}
		return navListItemRe.MatchString(l)
	}
	return false
}

// NavInstruction is the paragraph a destination that a model publishes to (an
// agent, an API or MCP integration) is handed about the document's navigation:
// rebuild it the destination's own way, and never leave a link to an anchor
// the published page will not have. "" when there is nothing to rebuild.
func NavInstruction(n DocNav) string {
	if n.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Navigation within the document. Its links to its own sections and its table of contents use anchors from where it was written, which the published page will not have. Rebuild them the way this destination does: ")
	if n.Contents != "" {
		fmt.Fprintf(&b, "replace the list under %q with the destination's own table of contents if it has one (a table-of-contents macro or block), otherwise with links to the headings in the destination's anchor format; ", n.Contents)
	}
	b.WriteString("make each link below point at its heading in the destination's anchor format. Where the destination cannot link within a page, keep the link's words as plain text. Do not change any other wording.\n")
	seen := map[string]bool{}
	for _, l := range n.Links {
		key := l.Text + "\x00" + l.Anchor
		if seen[key] {
			continue
		}
		seen[key] = true
		if l.Heading != "" {
			fmt.Fprintf(&b, "- %q (#%s) points at the heading %q\n", l.Text, l.Anchor, l.Heading)
		} else {
			fmt.Fprintf(&b, "- %q (#%s) points at no heading in the document: keep its words as plain text\n", l.Text, l.Anchor)
		}
	}
	return b.String()
}
