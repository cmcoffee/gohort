package anvil

// Where edit_file's old_text lands. An exact match is still the rule, but a
// miss used to be final even when the anchor differed from the file only in
// ways the model cannot see or does not reproduce: the file's CRLF line
// endings, a curly quote or an en dash it wrote as a plain one, spaces left
// at the end of a line. Each of those cost a round of reading the hint and
// copying again, and a local model often wrote the same plain quote again.
//
// So a miss is retried, in order, more loosely: with the file's CRLF line
// endings, then reading typographic quotes, dashes and spaces as plain ones,
// then ignoring spaces at the ends of lines. The first reading that finds the
// anchor decides, and it must still find it exactly once (or replace_all).
// Leading indentation is never loosened: in Python or YAML it is the code.
// The result says which reading matched, so the agent knows the file is not
// byte for byte what it wrote, and the diff card shows what went in.

import (
	"strings"
	"unicode/utf8"
)

// matchTier is how loosely old_text was read to find it.
type matchTier int

const (
	matchExact matchTier = iota
	matchLineEndings
	matchTypography
	matchTrailingSpace
)

// note is what the edit's result adds about a loose match; "" for exact.
func (t matchTier) note() string {
	switch t {
	case matchLineEndings:
		return " Matched with the file's CRLF line endings, which the new text keeps."
	case matchTypography:
		return " Matched only after reading the file's curly quotes, dashes or special spaces as plain ones: the new text went in as you wrote it."
	case matchTrailingSpace:
		return " Matched only after ignoring spaces at the ends of lines: the new text went in as you wrote it."
	}
	return ""
}

// span is a byte range of the file.
type span struct{ start, end int }

// anchorMatch is where old_text is in a file and what to put there.
type anchorMatch struct {
	spans []span // non-overlapping, in file order
	tier  matchTier
	// replacement is new_text, with CRLF line endings when the file has them
	// and new_text was written without.
	replacement string
}

// findAnchor finds oldText in content, exactly first and then more loosely.
// No spans means not found at any tier.
func findAnchor(content, oldText, newText string) anchorMatch {
	if s := occurrences(content, oldText); len(s) > 0 {
		return anchorMatch{spans: s, tier: matchExact, replacement: newText}
	}
	crlf := usesCRLF(content) && !strings.Contains(oldText, "\r")
	repl := newText
	if crlf && !strings.Contains(newText, "\r") {
		repl = strings.ReplaceAll(newText, "\n", "\r\n")
	}
	want := oldText
	if crlf {
		want = strings.ReplaceAll(oldText, "\n", "\r\n")
		if s := occurrences(content, want); len(s) > 0 {
			return anchorMatch{spans: s, tier: matchLineEndings, replacement: repl}
		}
	}
	norm, offs := normalizeTypography(content)
	if wantNorm, _ := normalizeTypography(want); norm != content || wantNorm != want {
		if s := occurrences(norm, wantNorm); len(s) > 0 {
			for i := range s {
				s[i] = span{offs[s[i].start], offs[s[i].end]}
			}
			return anchorMatch{spans: s, tier: matchTypography, replacement: repl}
		}
	}
	if s := lineOccurrences(content, oldText); len(s) > 0 {
		return anchorMatch{spans: s, tier: matchTrailingSpace, replacement: repl}
	}
	return anchorMatch{replacement: repl}
}

// apply replaces every span with the replacement.
func (m anchorMatch) apply(content string) string {
	var b strings.Builder
	last := 0
	for _, s := range m.spans {
		b.WriteString(content[last:s.start])
		b.WriteString(m.replacement)
		last = s.end
	}
	b.WriteString(content[last:])
	return b.String()
}

// occurrences are the non-overlapping places want is in content.
func occurrences(content, want string) []span {
	if want == "" {
		return nil
	}
	var out []span
	for off := 0; ; {
		i := strings.Index(content[off:], want)
		if i < 0 {
			return out
		}
		out = append(out, span{off + i, off + i + len(want)})
		off += i + len(want)
	}
}

// usesCRLF reports whether most of content's line breaks are CRLF.
func usesCRLF(content string) bool {
	crlf := strings.Count(content, "\r\n")
	return crlf > 0 && crlf >= strings.Count(content, "\n")-crlf
}

// typographic maps a character a model tends to write plainly to the plain
// one.
func typographic(r rune) (rune, bool) {
	switch r {
	case '\u2018', '\u2019', '\u201A', '\u201B':
		return '\'', true
	case '\u201C', '\u201D', '\u201E', '\u201F':
		return '"', true
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
		return '-', true
	case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007',
		'\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
		return ' ', true
	}
	return r, false
}

// normalizeTypography is s with typographic characters made plain, and for
// each byte of the result (and one past its end) the offset in s it came
// from, so a match in the plain text maps back to the original.
func normalizeTypography(s string) (string, []int) {
	var b strings.Builder
	offs := make([]int, 0, len(s)+1)
	for i, r := range s {
		if p, ok := typographic(r); ok {
			b.WriteRune(p)
			offs = append(offs, i)
			continue
		}
		// The rune's width in s, not r's: an invalid byte reads as
		// utf8.RuneError but is one byte long.
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		for k := 0; k < size; k++ {
			offs = append(offs, i+k)
		}
	}
	offs = append(offs, len(s))
	return b.String(), offs
}

// lineOccurrences finds oldText as whole lines of content, comparing each
// line without the spaces at its end and with typography made plain. A
// trailing newline on oldText takes the matched last line's break with it.
func lineOccurrences(content, oldText string) []span {
	want := strings.Split(strings.ReplaceAll(oldText, "\r\n", "\n"), "\n")
	withBreak := strings.HasSuffix(oldText, "\n")
	if withBreak {
		want = want[:len(want)-1]
	}
	if len(want) == 0 {
		return nil
	}
	key := func(l string) string {
		n, _ := normalizeTypography(strings.TrimRight(l, " \t\r"))
		return n
	}
	for i := range want {
		want[i] = key(want[i])
	}
	if strings.Join(want, "") == "" {
		return nil
	}

	// Each line's start, the end of its text (before \r\n or \n), and its
	// end including the break.
	type line struct{ start, text, end int }
	var lines []line
	for off := 0; off < len(content); {
		nl := strings.IndexByte(content[off:], '\n')
		if nl < 0 {
			lines = append(lines, line{off, len(content), len(content)})
			break
		}
		text := off + nl
		if text > off && content[text-1] == '\r' {
			text--
		}
		lines = append(lines, line{off, text, off + nl + 1})
		off += nl + 1
	}

	var out []span
	for i := 0; i+len(want) <= len(lines); i++ {
		ok := true
		for k := range want {
			l := lines[i+k]
			if key(content[l.start:l.text]) != want[k] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		last := lines[i+len(want)-1]
		end := last.text
		if withBreak {
			if last.end == last.text {
				continue // the file's last line has no break to take
			}
			end = last.end
		}
		out = append(out, span{lines[i].start, end})
		i += len(want) - 1
	}
	return out
}

// alreadyApplied reports a miss whose new text is in the file already: an
// edit made once and sent again, which no anchor will fix.
func alreadyApplied(content, newText string) bool {
	if len(strings.TrimSpace(newText)) < 20 {
		return false
	}
	c, _ := normalizeTypography(content)
	n, _ := normalizeTypography(newText)
	return strings.Contains(c, n) ||
		strings.Contains(strings.ReplaceAll(c, "\r\n", "\n"), strings.ReplaceAll(n, "\r\n", "\n"))
}
