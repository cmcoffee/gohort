// Reading a published copy back, so edits made THERE are not lost HERE.
//
// A document published to a wiki stops being the only place it is written.
// People and agents edit the page; the next publish from gohort used to write
// straight over them. Three pieces close that:
//
//   - PublishReader: a destination that can read its copy as it stands now.
//   - The publish guard: a destination about to UPDATE a copy shows what it
//     read first to CheckBeforeUpdate, and stops when the producer says no.
//     A destination that reads its copy after writing reports that with
//     ReportReadBack, which is how the producer knows what it last published
//     in the destination's own words (the baseline the next read compares to).
//   - PublishWatcher: the producer's side, registered per source kind. Every
//     publish passes through PublishDocument, which wires the watcher in, so
//     the Update button, a Publisher chat and an agent's publish tool are all
//     guarded the same way.
//
// SplitSections and ChangedSections compare two reads section by section. Two
// reads of one copy come back in the same format, so they are compared with
// each other, never with the markdown gohort sent: a converter's idea of a
// table is not an edit.

package docs

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// PublishedCopy is a published document as its destination returns it.
type PublishedCopy struct {
	// Body is the copy's content in the destination's own format: Confluence
	// storage XHTML, HTML, or markdown. SplitSections reads all three.
	Body string `json:"body"`
	// Version is the destination's version marker, when it has one. Two reads
	// with the same non-empty Version are the same copy.
	Version string `json:"version,omitempty"`
}

// PublishReader is implemented by a destination that can read a copy it
// published. ErrCannotReadBack says this destination (or this target of it)
// has no way to.
type PublishReader interface {
	ReadPublished(ctx context.Context, user string, rec PublishRecord) (PublishedCopy, error)
}

// ErrCannotReadBack is returned for a copy whose destination cannot read it.
var ErrCannotReadBack = errors.New("this destination cannot read its published copy back")

// ReadPublished reads the copy rec records, as it stands now.
func ReadPublished(ctx context.Context, user string, rec PublishRecord) (PublishedCopy, error) {
	d := lookupDest(rec.Kind)
	if d == nil {
		return PublishedCopy{}, ErrCannotReadBack
	}
	r, ok := d.(PublishReader)
	if !ok {
		return PublishedCopy{}, ErrCannotReadBack
	}
	if strings.TrimSpace(rec.ExternalID) == "" && strings.TrimSpace(rec.URL) == "" {
		return PublishedCopy{}, errors.New("there is no record of where it was published")
	}
	return r.ReadPublished(ctx, user, rec)
}

// PublishWatcher is a producer following its documents' published copies.
type PublishWatcher interface {
	// BeforeUpdate is shown the copy as it stands just before an update
	// overwrites it. An error stops the update and is its outcome.
	BeforeUpdate(ctx context.Context, user string, doc PublishDoc, kind string, cur PublishedCopy) error
	// Landed is told what a publish left at the destination. read is false
	// when the destination could not say, and cur is then empty.
	Landed(user string, doc PublishDoc, kind string, cur PublishedCopy, read bool)
}

var (
	watchersMu sync.RWMutex
	watchers   = map[string]PublishWatcher{}
)

// RegisterPublishWatcher follows every publish of documents whose
// PublishDoc.SourceKind is sourceKind.
func RegisterPublishWatcher(sourceKind string, w PublishWatcher) {
	watchersMu.Lock()
	watchers[sourceKind] = w
	watchersMu.Unlock()
}

func watcherFor(sourceKind string) PublishWatcher {
	watchersMu.RLock()
	defer watchersMu.RUnlock()
	return watchers[strings.TrimSpace(sourceKind)]
}

type guardKey struct{}
type readBackKey struct{}

// WithPublishGuard carries the check an update must pass on ctx.
// PublishDocument sets it for an update of a watched document.
func WithPublishGuard(ctx context.Context, fn func(PublishedCopy) error) context.Context {
	return context.WithValue(ctx, guardKey{}, fn)
}

// GuardsUpdate reports whether a publish on ctx checks the copy before it
// overwrites it: a destination that has to spend something to read first (a
// model run) reads only when this is true.
func GuardsUpdate(ctx context.Context) bool {
	fn, _ := ctx.Value(guardKey{}).(func(PublishedCopy) error)
	return fn != nil
}

// CheckBeforeUpdate is called by a destination with the copy it read just
// before overwriting it. A non-nil error means do not write.
func CheckBeforeUpdate(ctx context.Context, cur PublishedCopy) error {
	if fn, _ := ctx.Value(guardKey{}).(func(PublishedCopy) error); fn != nil {
		return fn(cur)
	}
	return nil
}

// withReadBack carries where a destination reports its copy after writing.
func withReadBack(ctx context.Context, fn func(PublishedCopy)) context.Context {
	return context.WithValue(ctx, readBackKey{}, fn)
}

// WantsReadBack reports whether anybody on ctx wants the copy read after it
// is written.
func WantsReadBack(ctx context.Context) bool {
	fn, _ := ctx.Value(readBackKey{}).(func(PublishedCopy))
	return fn != nil
}

// ReportReadBack is called by a destination with its copy as it reads after
// the write.
func ReportReadBack(ctx context.Context, cur PublishedCopy) {
	if fn, _ := ctx.Value(readBackKey{}).(func(PublishedCopy)); fn != nil {
		fn(cur)
	}
}

// watchPublish wires the watcher for req's document into ctx, and returns
// what to call when the publish has succeeded.
func watchPublish(ctx context.Context, user, kind string, req PublishRequest) (context.Context, func()) {
	w := watcherFor(req.Doc.SourceKind)
	if w == nil {
		return ctx, func() {}
	}
	if strings.TrimSpace(req.ExternalID) != "" {
		ctx = WithPublishGuard(ctx, func(cur PublishedCopy) error {
			return w.BeforeUpdate(ctx, user, req.Doc, kind, cur)
		})
	}
	var mu sync.Mutex
	var got *PublishedCopy
	ctx = withReadBack(ctx, func(cur PublishedCopy) {
		mu.Lock()
		got = &cur
		mu.Unlock()
	})
	return ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if got == nil {
			w.Landed(user, req.Doc, kind, PublishedCopy{}, false)
			return
		}
		w.Landed(user, req.Doc, kind, *got, true)
	}
}

// --- comparing two reads ---------------------------------------------------

// DocSection is one top-level part of a document: its heading and its text.
// The text before the first heading has an empty Title.
type DocSection struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// SectionChange is one section that differs between two reads.
type SectionChange struct {
	Kind   string // "changed", "added" or "removed"
	Title  string
	Before string
	After  string
}

var (
	cdataRE       = regexp.MustCompile(`(?s)<!\[CDATA\[(.*?)\]\]>`)
	htmlHeadingRE = regexp.MustCompile(`(?is)<h([12])\b[^>]*>(.*?)</h[12]\s*>`)
	blockEndRE    = regexp.MustCompile(`(?i)</(?:p|div|li|tr|pre|blockquote|h[1-6]|table|ul|ol|ac:structured-macro|ac:task)\s*>|<br\s*/?>`)
	cellEndRE     = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	anyTagRE      = regexp.MustCompile(`(?s)<[^>]+>`)
	mdHeadingRE   = regexp.MustCompile(`^(#{1,2})\s+(.*?)\s*#*\s*$`)
	blankRunRE    = regexp.MustCompile(`\n{3,}`)
	leadNumberRE  = regexp.MustCompile(`^\d+[.)]\s+`)
	looksHTMLRE   = regexp.MustCompile(`(?i)<(?:p|h[1-6]|div|ul|ol|table|ac:[a-z-]+)\b`)
)

// SplitSections splits a document at its level 1 and 2 headings. It reads
// HTML or Confluence storage format (by tags) and markdown (by # lines).
func SplitSections(body string) []DocSection {
	if looksHTMLRE.MatchString(body) {
		return splitHTMLSections(body)
	}
	return splitMarkdownSections(body)
}

func splitMarkdownSections(md string) []DocSection {
	var out []DocSection
	cur := DocSection{}
	var text strings.Builder
	inFence := false
	flush := func() {
		cur.Text = tidyText(text.String())
		if cur.Title != "" || cur.Text != "" {
			out = append(out, cur)
		}
		text.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence {
			if m := mdHeadingRE.FindStringSubmatch(line); m != nil {
				flush()
				cur = DocSection{Title: strings.TrimSpace(m[2])}
				continue
			}
		}
		text.WriteString(line + "\n")
	}
	flush()
	return out
}

func splitHTMLSections(s string) []DocSection {
	s = cdataRE.ReplaceAllString(s, "$1")
	var out []DocSection
	add := func(title, chunk string) {
		sec := DocSection{Title: strings.TrimSpace(htmlText(title)), Text: htmlText(chunk)}
		if sec.Title != "" || sec.Text != "" {
			out = append(out, sec)
		}
	}
	at, title := 0, ""
	for _, m := range htmlHeadingRE.FindAllStringSubmatchIndex(s, -1) {
		add(title, s[at:m[0]])
		title, at = s[m[4]:m[5]], m[1]
	}
	add(title, s[at:])
	return out
}

// htmlText is markup reduced to its words, a line per block.
func htmlText(s string) string {
	s = blockEndRE.ReplaceAllString(s, "\n")
	s = cellEndRE.ReplaceAllString(s, " | ")
	s = anyTagRE.ReplaceAllString(s, "")
	return tidyText(html.UnescapeString(s))
}

func tidyText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(blankRunRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// sectionKey matches a section across reads: its title without a leading
// number ("2. Setup" and "Setup" are one section), in lower case.
func sectionKey(title string) string {
	t := strings.ToLower(strings.Join(strings.Fields(title), " "))
	return leadNumberRE.ReplaceAllString(t, "")
}

// sameText compares two sections by their words, so spacing and line breaks
// are not edits.
func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// derivedSection reports a section a destination or the producer builds from
// the others (a table of contents): its changes follow theirs.
func derivedSection(title string) bool {
	switch sectionKey(title) {
	case "contents", "table of contents":
		return true
	}
	return false
}

// ChangedSections lists the sections that differ from before to after, in
// after's order, removed ones last.
func ChangedSections(before, after []DocSection) []SectionChange {
	was := map[string]DocSection{}
	for _, s := range before {
		was[sectionKey(s.Title)] = s
	}
	var out []SectionChange
	seen := map[string]bool{}
	for _, s := range after {
		k := sectionKey(s.Title)
		seen[k] = true
		if derivedSection(s.Title) {
			continue
		}
		old, ok := was[k]
		switch {
		case !ok:
			out = append(out, SectionChange{Kind: "added", Title: s.Title, After: s.Text})
		case !sameText(old.Text, s.Text):
			out = append(out, SectionChange{Kind: "changed", Title: s.Title, Before: old.Text, After: s.Text})
		}
	}
	for _, s := range before {
		if k := sectionKey(s.Title); !seen[k] && !derivedSection(s.Title) {
			out = append(out, SectionChange{Kind: "removed", Title: s.Title, Before: s.Text})
		}
	}
	return out
}

// --- reading a copy out of a tool's reply -----------------------------------

var httpEnvelopeRE = regexp.MustCompile(`^HTTP \d{3}[^\n]*\n`)

// BodyFromToolResult pulls a document's body out of what a tool returned
// when it read the copy: an API reply ("HTTP 200 OK" and JSON), or an MCP
// tool's JSON or text. In JSON the body is the longest string in it, which
// for a page is its content, not its title or id. A version number found
// beside it ("version": {"number": 7} or "version": 7) is returned too.
func BodyFromToolResult(out string) PublishedCopy {
	out = strings.TrimSpace(httpEnvelopeRE.ReplaceAllString(strings.TrimSpace(out), ""))
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return PublishedCopy{Body: out}
	}
	body := longestString(v)
	if body == "" {
		body = out
	}
	return PublishedCopy{Body: body, Version: versionOf(v)}
}

func longestString(v any) string {
	best := ""
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		for _, x := range t {
			if s := longestString(x); len(s) > len(best) {
				best = s
			}
		}
	case []any:
		for _, x := range t {
			if s := longestString(x); len(s) > len(best) {
				best = s
			}
		}
	}
	return best
}

// versionOf finds a top-level version marker, or "".
func versionOf(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	switch ver := m["version"].(type) {
	case float64:
		return strconv.Itoa(int(ver))
	case string:
		return ver
	case map[string]any:
		if n, ok := ver["number"].(float64); ok {
			return strconv.Itoa(int(n))
		}
	}
	return ""
}
