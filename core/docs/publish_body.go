package docs

// The document travels by reference.
//
// A destination a model publishes to through an integration (an API, an MCP
// server's tools) used to be handed the whole document in its instruction,
// and the model then typed all of it back out into the API call, converted to
// the destination's format. That is output the size of the document at the
// model's speed, with the document three times over in its input on every
// round (the instruction, the page read before an update, the page read
// after). A long guide outran a single generation's time limit while the
// model was still typing, and nothing on screen moved meanwhile: tool-call
// arguments are not thinking and not a reply.
//
// Now the model writes a placeholder where the body goes, and the framework
// puts the document in when the call is sent (ExpandArgs). The model still
// decides everything about the call, the endpoint, the fields, what goes
// around the body; it only stops carrying the bytes. It is shown the
// document's outline instead of its text, and pages it reads come back with
// their long text clipped (ClipForModel), the whole copy kept for the checks
// that compare it.
//
// Links within the document were rebuilt by the model as it typed. Here it
// names the destination's anchors (SetLinks) and the framework rewrites them,
// which is also the end of a model "fixing" wording on the way through.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/textutil"
)

// The placeholders a model writes where the document's body goes.
const (
	BodyMarkdownPlaceholder = "{{document}}"
	BodyHTMLPlaceholder     = "{{document_html}}"
)

// PublishBody is the document one publish carries by reference.
type PublishBody struct {
	doc PublishDoc
	nav DocNav

	mu          sync.Mutex
	links       map[string]string // anchor -> where it points now; "" keeps the words as text
	contents    string            // markup in place of the document's own contents list
	contentsSet bool
	expanded    int // calls that carried the document
}

type publishBodyKey struct{}

// WithPublishBody carries req's document on ctx for the integration run to
// put into its calls.
func WithPublishBody(ctx context.Context, req PublishRequest) context.Context {
	b := &PublishBody{doc: req.Doc, nav: req.Nav}
	// What the destination named last time is where this one starts: an
	// update whose run does not name them again keeps them, where it used to
	// send the document's own list and leave the destination's table of
	// contents behind.
	if len(req.NavLinks) > 0 {
		b.links = map[string]string{}
		for a, to := range req.NavLinks {
			b.links[a] = to
		}
	}
	if req.NavContents != nil {
		b.contents, b.contentsSet = *req.NavContents, true
	}
	return context.WithValue(ctx, publishBodyKey{}, b)
}

// Navigation is how this publish writes the document's navigation at the
// destination, as SetLinks left it (or as the last publish did): for the
// producer to keep and hand to the next update.
func (b *PublishBody) Navigation() (map[string]string, *string) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var links map[string]string
	if len(b.links) > 0 {
		links = map[string]string{}
		for a, to := range b.links {
			links[a] = to
		}
	}
	var contents *string
	if b.contentsSet {
		c := b.contents
		contents = &c
	}
	return links, contents
}

// PublishBodyFrom is the document ctx carries by reference, or nil.
func PublishBodyFrom(ctx context.Context) *PublishBody {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(publishBodyKey{}).(*PublishBody)
	return b
}

// SetLinks records where the document's links to its own sections point at
// the destination: anchor (with or without "#") to the new target, "" for
// plain words. contents, when given, is the markup to put in place of the
// document's own table of contents ("" drops the list).
func (b *PublishBody) SetLinks(links map[string]string, contents *string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.links == nil {
		b.links = map[string]string{}
	}
	for a, to := range links {
		b.links[strings.TrimPrefix(strings.TrimSpace(a), "#")] = strings.TrimSpace(to)
	}
	if contents != nil {
		b.contents, b.contentsSet = *contents, true
	}
}

// Expanded is how many calls carried the document.
func (b *PublishBody) Expanded() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.expanded
}

// contentsMarker stands in for the contents list until the destination's
// markup replaces it: plain letters, so no renderer escapes it.
const contentsMarker = "GOHORTCONTENTSLIST7F3A"

// markdown is the document as it goes out, links rewritten, with the
// contents list replaced by the marker when the destination gave markup.
func (b *PublishBody) markdown() (md string, marked bool) {
	b.mu.Lock()
	links, contents, contentsSet := b.links, b.contents, b.contentsSet
	b.mu.Unlock()
	md = b.doc.Markdown
	if len(links) > 0 {
		md = navLinkRe.ReplaceAllStringFunc(md, func(m string) string {
			sub := navLinkRe.FindStringSubmatch(m)
			to, ok := links[sub[2]]
			switch {
			case !ok:
				return m
			case to == "":
				return sub[1]
			}
			return "[" + sub[1] + "](" + to + ")"
		})
	}
	if contentsSet && b.nav.Contents != "" {
		md, marked = replaceContentsList(md, b.nav.Contents, contents != "")
	}
	return md, marked
}

// replaceContentsList takes out the list under the heading titled title,
// leaving the marker in its place when mark is set.
func replaceContentsList(md, title string, mark bool) (string, bool) {
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		m := navHeadingRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || strings.TrimSpace(m[1]) != title {
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		k := j
		for k < len(lines) && (navListItemRe.MatchString(lines[k]) || (k > j && strings.HasPrefix(lines[k], "  ") && strings.TrimSpace(lines[k]) != "")) {
			k++
		}
		if k == j {
			return md, false
		}
		var repl []string
		if mark {
			repl = []string{contentsMarker}
		}
		out := append(append(append([]string{}, lines[:j]...), repl...), lines[k:]...)
		return strings.Join(out, "\n"), mark
	}
	return md, false
}

// Markdown is the document's Markdown as it goes out.
func (b *PublishBody) Markdown() string {
	md, marked := b.markdown()
	if marked {
		md = strings.Replace(md, contentsMarker, b.contentsMarkup(), 1)
	}
	return md
}

// HTML is the document's HTML as it goes out: the producer's own rendering
// when it has one and nothing was rewritten, else rendered here.
func (b *PublishBody) HTML() string {
	b.mu.Lock()
	rewritten := len(b.links) > 0 || b.contentsSet
	b.mu.Unlock()
	if !rewritten && strings.TrimSpace(b.doc.HTML) != "" {
		return b.doc.HTML
	}
	md, marked := b.markdown()
	html := textutil.MarkdownToHTML(md)
	if marked {
		html = strings.Replace(html, "<p>"+contentsMarker+"</p>", b.contentsMarkup(), 1)
		html = strings.Replace(html, contentsMarker, b.contentsMarkup(), 1)
	}
	return html
}

func (b *PublishBody) contentsMarkup() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.contents
}

// ExpandArgs puts the document in where args name it, and reports whether
// any did. A string that is itself JSON (an API body written as text) is
// expanded inside its values, so the document lands escaped as JSON needs.
func (b *PublishBody) ExpandArgs(args map[string]any) (map[string]any, bool) {
	if b == nil || len(args) == 0 {
		return args, false
	}
	var md, html *string
	get := func(which string) string {
		if which == BodyHTMLPlaceholder {
			if html == nil {
				s := b.HTML()
				html = &s
			}
			return *html
		}
		if md == nil {
			s := b.Markdown()
			md = &s
		}
		return *md
	}
	out, hit := expandValue(args, get)
	if hit {
		b.mu.Lock()
		b.expanded++
		b.mu.Unlock()
	}
	m, _ := out.(map[string]any)
	return m, hit
}

func hasPlaceholder(s string) bool {
	return strings.Contains(s, BodyMarkdownPlaceholder) || strings.Contains(s, BodyHTMLPlaceholder)
}

func expandValue(v any, get func(string) string) (any, bool) {
	switch t := v.(type) {
	case string:
		if !hasPlaceholder(t) {
			return t, false
		}
		if s := strings.TrimSpace(t); (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")) && json.Valid([]byte(s)) {
			var inner any
			if json.Unmarshal([]byte(s), &inner) == nil {
				if x, hit := expandValue(inner, get); hit {
					var buf bytes.Buffer
					enc := json.NewEncoder(&buf)
					enc.SetEscapeHTML(false)
					if enc.Encode(x) == nil {
						return strings.TrimSuffix(buf.String(), "\n"), true
					}
				}
			}
		}
		t = strings.ReplaceAll(t, BodyHTMLPlaceholder, get(BodyHTMLPlaceholder))
		t = strings.ReplaceAll(t, BodyMarkdownPlaceholder, get(BodyMarkdownPlaceholder))
		return t, true
	case map[string]any:
		out := make(map[string]any, len(t))
		hitAny := false
		for k, x := range t {
			nx, hit := expandValue(x, get)
			out[k] = nx
			hitAny = hitAny || hit
		}
		return out, hitAny
	case []any:
		out := make([]any, len(t))
		hitAny := false
		for i, x := range t {
			nx, hit := expandValue(x, get)
			out[i] = nx
			hitAny = hitAny || hit
		}
		return out, hitAny
	}
	return v, false
}

// ReferenceInstruction is what a model publishing through an integration is
// told about the document in place of its text: the placeholders, the
// outline, how it begins, and its links to rebuild.
func ReferenceInstruction(req PublishRequest) string {
	md := req.Doc.Markdown
	var b strings.Builder
	fmt.Fprintf(&b, "The document is not pasted here, and you must not type it out. Where the API takes the document's body, write %s for its Markdown or %s for it as HTML, exactly so, inside the value you send. When the call goes out it is replaced with the whole document, so a long document costs you nothing to send. You may put things around it, a table-of-contents macro before it for example. A call that sends the body without a placeholder sends only what you typed.\n\n",
		BodyMarkdownPlaceholder, BodyHTMLPlaceholder)
	fmt.Fprintf(&b, "The document: %d words", len(strings.Fields(md)))
	if hs := req.Nav.Headings; len(hs) > 0 {
		fmt.Fprintf(&b, ", %d headings:\n", len(hs))
		for i, h := range hs {
			if i == 60 {
				fmt.Fprintf(&b, "- and %d more\n", len(hs)-60)
				break
			}
			fmt.Fprintf(&b, "- %s\n", h.Text)
		}
	} else {
		b.WriteString(".\n")
	}
	if lead := docLead(md, 400); lead != "" {
		b.WriteString("It begins: " + lead + "\n")
	}
	if nav := referenceNavInstruction(req.Nav); nav != "" {
		b.WriteString("\n" + nav)
	}
	return b.String()
}

// docLead is the first prose of md, up to max characters, on one line.
func docLead(md string, max int) string {
	var parts []string
	n := 0
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "|") {
			continue
		}
		parts = append(parts, t)
		n += len(t)
		if n >= max {
			break
		}
	}
	s := strings.Join(parts, " ")
	s = textutil.Clip(s, max)
	return s
}

// referenceNavInstruction is NavInstruction for a document the model does not
// type: it names the anchors, set_document_links rewrites them.
func referenceNavInstruction(n DocNav) string {
	if n.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Navigation within the document. Its links to its own sections use anchors from where it was written, which the published page will not have. If this destination names heading anchors its own way, call set_document_links before you send the document, with one line per link, \"#anchor -> where it should point\", or \"#anchor ->\" to keep the link's words as plain text where the destination cannot link within a page. Links you do not name go out as written.")
	if n.Contents != "" {
		fmt.Fprintf(&b, " Its table of contents is the list under %q: give set_document_links the destination's own table-of-contents markup as contents to put in its place, or an empty contents to drop the list; leave it out to keep the list.", n.Contents)
	}
	b.WriteString("\n")
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
			fmt.Fprintf(&b, "- %q (#%s) points at no heading in the document\n", l.Text, l.Anchor)
		}
	}
	return b.String()
}

// ParseLinkLines reads set_document_links' lines: "#anchor -> target".
func ParseLinkLines(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		a, to, ok := strings.Cut(line, "->")
		a = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(a), "-* "))
		if !ok || a == "" {
			continue
		}
		out[strings.TrimPrefix(a, "#")] = strings.TrimSpace(to)
	}
	return out
}

// clipKeep is how much of one long text a model is shown of a result.
const clipKeep = 600

var clipRE = regexp.MustCompile(`^(HTTP \d{3}[^\n]*\n)?`)

// ClipForModel is a tool result as the model sees it while the document
// travels by reference: a JSON reply keeps its shape and every short field
// (ids, versions, links), its long text cut to its start; anything else is
// cut to its start. n is the result's number, which the checks that compare
// the page use to reach the whole copy.
func ClipForModel(out string, n, max int) string {
	if len(out) <= max {
		return out
	}
	env := clipRE.FindString(out)
	rest := strings.TrimSpace(out[len(env):])
	var v any
	if json.Unmarshal([]byte(rest), &v) == nil {
		cut := clipLongStrings(v, n)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(cut) == nil && buf.Len() <= max*2 {
			return env + strings.TrimSuffix(buf.String(), "\n")
		}
	}
	return fmt.Sprintf("%s\n[cut here: %d characters in all, kept whole as result %d]", out[:max], len(out), n)
}

func clipLongStrings(v any, n int) any {
	switch t := v.(type) {
	case string:
		if len(t) > clipKeep*2 {
			return fmt.Sprintf("%s [cut here: %d characters in all, kept whole as result %d]", t[:clipKeep], len(t), n)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = clipLongStrings(x, n)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = clipLongStrings(x, n)
		}
		return out
	}
	return v
}
