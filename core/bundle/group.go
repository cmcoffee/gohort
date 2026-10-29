// Grouping lines by shape: what a log says, without how many times or to whom.
//
// The first question asked of an unfamiliar bundle is rarely "find X". It is
// "what is in here, and what is unusual", and a search cannot answer it: the
// asker has to already know the pattern. A million lines of a log are mostly a
// few dozen messages repeated with different numbers in them, so masking the
// numbers and counting what is left turns the bundle into a list short enough
// to read, with the rare shapes, usually the interesting ones, standing out at
// the bottom of it.
//
// The masking is deliberately one plain rule rather than a catalogue of id
// formats. A line splits into atoms (runs of letters, digits and . : - _) and
// any atom holding a digit is a variable: a timestamp, an address and port, a
// uuid, a hex id, "12ms", "worker-3" all become <*>, while "id=", "sshd[" and
// the message words around them stay. What it gets wrong it gets wrong the
// cheap way: a variable with no digit in it (a user name) splits a group into
// several, which shows as more rows, never as lines silently merged away.
package bundle

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxShapeBytes caps a shape. Lines that agree for this long are one
	// group: a JSON line whose tail is a payload would otherwise be a shape
	// of its own, every one of them.
	maxShapeBytes = 200
	// maxGroups bounds the groups one search keeps. Past it a line with a
	// new shape is counted as ungrouped rather than grown into a map the
	// size of the log, which is what a log with no repeated shapes would do.
	maxGroups = 5000
	// maxExampleBytes caps the example line a group keeps. Copied, never
	// sliced from the line it came from: a bundle line is a piece of its
	// whole decrypted slice, and a group holding it would hold the slice.
	maxExampleBytes = 1 << 10
	// GroupsShown is how many of the most frequent shapes a reply lists,
	// and GroupsRarest how many of the rarest it lists after them.
	GroupsShown  = 40
	GroupsRarest = 10
	// variable is the placeholder a masked atom becomes.
	variable = "<*>"
)

// Group is one shape and what was seen of it.
type Group struct {
	Shape string
	Count int
	Files int // distinct files it appeared in
	// Example is the first line seen with this shape, as Path:Line.
	Path string
	Line int
	Text string
	// First and Last are the earliest and latest timestamps among its
	// lines; zero when none of them carried one.
	First, Last time.Time
	lastPath    string
}

// Grouper collects matched lines into groups by shape. Add is called once per
// line, in file order; a file's lines are expected to arrive together, which
// is how Files is counted without a set per group.
type Grouper struct {
	groups    map[string]*Group
	buf       []byte
	Lines     int // lines added
	Ungrouped int // lines whose new shape arrived after maxGroups were held
}

// NewGrouper makes an empty Grouper.
func NewGrouper() *Grouper { return &Grouper{groups: map[string]*Group{}} }

// Add files one line under its shape. text is the line, path and line its
// place, when its timestamp (zero for none).
func (g *Grouper) Add(path string, line int, text string, when time.Time) {
	addLine(g, path, line, text, when)
}

// AddBytes is Add for a line held as bytes, which makes no string for a line
// whose shape is already known.
func (g *Grouper) AddBytes(path string, line int, text []byte, when time.Time) {
	addLine(g, path, line, text, when)
}

func addLine[T string | []byte](g *Grouper, path string, line int, text T, when time.Time) {
	g.Lines++
	g.buf = appendShape(g.buf[:0], text)
	// The string(g.buf) in a map index does not allocate; only a new
	// group's key does.
	grp, ok := g.groups[string(g.buf)]
	if !ok {
		if len(g.groups) >= maxGroups {
			g.Ungrouped++
			return
		}
		grp = &Group{Shape: string(g.buf), Path: path, Line: line, Text: example(text)}
		g.groups[grp.Shape] = grp
	}
	grp.Count++
	if grp.lastPath != path {
		grp.Files++
		grp.lastPath = path
	}
	if !when.IsZero() {
		if grp.First.IsZero() || when.Before(grp.First) {
			grp.First = when
		}
		if when.After(grp.Last) {
			grp.Last = when
		}
	}
}

// Len is how many distinct shapes were kept.
func (g *Grouper) Len() int { return len(g.groups) }

// Sorted returns every group, most frequent first, ties by shape so the order
// is the same every run.
func (g *Grouper) Sorted() []Group {
	out := make([]Group, 0, len(g.groups))
	for _, grp := range g.groups {
		out = append(out, *grp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Shape < out[j].Shape
	})
	return out
}

// Render formats the groups for a model: the most frequent shapes, then, when
// there are more than fit, the rarest, since an investigation is as often
// after the one odd line as the flood. maxText cuts an example line for
// display.
func (g *Grouper) Render(maxText int) string {
	all := g.Sorted()
	var b strings.Builder
	if len(all) == 0 {
		return ""
	}
	fmt.Fprintf(&b, "%d line(s) matched, in %d shape(s). Numbers, ids, addresses and times are masked as %s, so lines differing only in those are one shape.\n",
		g.Lines, len(all), variable)
	top, rare := all, []Group(nil)
	if len(all) > GroupsShown+GroupsRarest {
		top, rare = all[:GroupsShown], all[len(all)-GroupsRarest:]
	}
	b.WriteString("\nMost frequent first:\n")
	for _, grp := range top {
		writeGroup(&b, grp, maxText)
	}
	if rare != nil {
		fmt.Fprintf(&b, "\n...%d shape(s) between, not listed. The rarest:\n", len(all)-len(top)-len(rare))
		for _, grp := range rare {
			writeGroup(&b, grp, maxText)
		}
	}
	if g.Ungrouped > 0 {
		fmt.Fprintf(&b, "\n%d more line(s) had shapes past the first %d and were counted but not grouped. Narrow the pattern, or exclude the common shapes, to group them.\n", g.Ungrouped, maxGroups)
	}
	b.WriteString("\nTo look past a frequent shape, put a regular expression for it in exclude and group again; to read one, search for its example's words.\n")
	return b.String()
}

func writeGroup(b *strings.Builder, grp Group, maxText int) {
	fmt.Fprintf(b, "\n%8d x  %s\n", grp.Count, grp.Shape)
	span := fmt.Sprintf("%d file(s)", grp.Files)
	if !grp.First.IsZero() {
		span += ", " + grp.First.Format(time.RFC3339)
		if !grp.Last.Equal(grp.First) {
			span += " to " + grp.Last.Format(time.RFC3339)
		}
	}
	fmt.Fprintf(b, "            %s\n", span)
	fmt.Fprintf(b, "            e.g. %s:%d: %s\n", grp.Path, grp.Line, cutText(grp.Text, maxText))
}

// example copies a line for a group to keep, cut to maxExampleBytes on a rune
// boundary.
func example[T string | []byte](text T) string {
	n := len(text)
	if n > maxExampleBytes {
		n = maxExampleBytes
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
	}
	return string(append([]byte(nil), text[:n]...))
}

// cutText cuts s to max runes, marked, without splitting a rune.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// Shape is a line with its variable parts masked: see the file comment.
func Shape(line string) string { return string(appendShape(nil, line)) }

// appendShape appends line's shape to dst. Bytes outside ASCII are copied as
// they are, so a multi-byte rune is never split by the masking; the length cap
// backs up to a rune boundary for the same reason.
func appendShape[T string | []byte](dst []byte, line T) []byte {
	start := len(dst)
	i := 0
	for i < len(line) && len(dst)-start < maxShapeBytes {
		if !isAtom(line[i]) {
			dst = append(dst, line[i])
			i++
			continue
		}
		j, digit := i, false
		for j < len(line) && isAtom(line[j]) {
			if '0' <= line[j] && line[j] <= '9' {
				digit = true
			}
			j++
		}
		if digit {
			// Punctuation trailing the value is the sentence's, not the
			// value's: "round 1: starting" is "round <*>: starting".
			k := j
			for k > i+1 && isSeparator(line[k-1]) {
				k--
			}
			dst = append(dst, variable...)
			dst = append(dst, line[k:j]...)
		} else {
			dst = append(dst, line[i:j]...)
		}
		i = j
	}
	if i < len(line) {
		cut := start + maxShapeBytes
		if len(dst) > cut {
			for cut > start && !utf8.RuneStart(dst[cut]) {
				cut--
			}
			dst = dst[:cut]
		}
		dst = append(dst, "…"...)
	}
	return dst
}

// isAtom reports whether c continues a word-like run: an identifier, a number,
// a dotted or colon-separated value.
func isAtom(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return isSeparator(c)
}

// isSeparator reports whether c joins the parts of an atom rather than
// spelling one: the . : - _ of a version, an address, a uuid, a timestamp.
func isSeparator(c byte) bool { return c == '.' || c == ':' || c == '-' || c == '_' }
