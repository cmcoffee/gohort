// Markdown → plain-text flattening, shared by any transport that delivers to a
// surface which renders no markdown (iMessage, SMS). Lifted from phantom's
// markdown_chunks.go so bridges can reuse it without depending on the retiring
// phantom app. Strips formatting while preserving every word of content.

package textutil

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Inline patterns. Run on each line.
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`) // [text](url) → "text (url)"
	mdBold    = regexp.MustCompile(`\*\*([^*]+)\*\*`)         // **x** → x
	mdItalic1 = regexp.MustCompile(`\*([^*]+)\*`)             // *x* → x
	// _x_ → x, but ONLY at word boundaries. Intra-word underscores are NOT
	// emphasis in CommonMark, so snake_case identifiers (link_entities,
	// recall_about, store_fact) must survive to plain-text transports. A naive
	// `_([^_]+)_` pairs the underscores across two such tokens on one line and
	// silently eats them (link_entities … recall_about → linkentities …
	// recallabout). Boundary chars are captured ($1/$3) and re-emitted.
	mdItalic2  = regexp.MustCompile(`(^|[^\w])_([^_\n]+)_([^\w]|$)`)
	mdInlineCo = regexp.MustCompile("`([^`]+)`")                // `x` → x
	mdHeader   = regexp.MustCompile(`^#{1,6}\s+(.+)$`)          // # Title → Title
	mdBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.+)$`)      // - item / * item → • item
	mdQuote    = regexp.MustCompile(`^>\s*(.*)$`)               // > x → x
	mdHR       = regexp.MustCompile(`^(\s*)([-*_]\s*){3,}\s*$`) // --- → blank
)

// MarkdownToPlain strips markdown formatting while preserving every word of
// content. Headers keep their text + surrounding blank lines; code fences strip
// the ``` but preserve the body; block quotes lose the `>` prefix; bullets become
// "• ". Numbered lists are already readable, so they pass through unchanged.
func MarkdownToPlain(md string) string {
	var out strings.Builder
	inCode := false
	lines := strings.Split(md, "\n")
	for _, ln := range lines {
		// Code fence boundary — strip the line; toggle the in-code flag so
		// inline-strippers don't munge the content inside.
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inCode = !inCode
			out.WriteByte('\n')
			continue
		}
		if inCode {
			// Preserve code-block content verbatim.
			out.WriteString(ln)
			out.WriteByte('\n')
			continue
		}
		// Headers — keep the text, drop the # markers. Add a blank line above
		// so the title visually separates.
		if m := mdHeader.FindStringSubmatch(ln); m != nil {
			if out.Len() > 0 {
				trimmed := strings.TrimRight(out.String(), "\n")
				out.Reset()
				out.WriteString(trimmed)
				out.WriteString("\n\n")
			}
			out.WriteString(m[1])
			out.WriteString("\n")
			continue
		}
		// Horizontal rules → blank line.
		if mdHR.MatchString(ln) {
			out.WriteByte('\n')
			continue
		}
		// Block quote prefix → drop.
		if m := mdQuote.FindStringSubmatch(ln); m != nil {
			ln = m[1]
		}
		// Bullets → "• " for readability.
		if m := mdBullet.FindStringSubmatch(ln); m != nil {
			ln = m[1] + "• " + m[2]
		}
		// Inline replacements.
		ln = mdLink.ReplaceAllString(ln, "$1 ($2)")
		ln = mdBold.ReplaceAllString(ln, "$1")
		ln = mdItalic1.ReplaceAllString(ln, "$1")
		ln = mdItalic2.ReplaceAllString(ln, "${1}${2}${3}")
		ln = mdInlineCo.ReplaceAllString(ln, "$1")
		out.WriteString(ln)
		out.WriteByte('\n')
	}
	// Collapse 3+ consecutive newlines to 2 (one blank line max).
	result := out.String()
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

var (
	spokenURL       = regexp.MustCompile(`(https?://|www\.)\S*[^\s.,;:!?)'"]`) // not the punctuation after it
	spokenSpaces    = regexp.MustCompile(`[ \t]{2,}`)
	spokenEmptyPair = regexp.MustCompile(`\(\s*\)`)
	spokenLoosePunc = regexp.MustCompile(`[ \t]+([.,;:!?)])`)
	// Emoji, and what joins and shapes them (the variation selector, the
	// zero-width joiner, a keycap, a flag's tags).
	spokenEmoji = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}\x{200D}\x{20E3}\x{E0020}-\x{E007F}]+`)
	// A smiley written in text, standing on its own: :) ;-) :D :P <3.
	spokenSmiley = regexp.MustCompile(`(^|[ \t])(?:[:;=][-']?[()DPp/|\]\[]|<3|\^_\^)([ \t]|$|[.,!?])`)
)

// SpeakableText is MarkdownToPlain for a voice: the part of a reply worth
// reading aloud. A speech model reads a code block or a web address out
// character by character and a listener can use neither, so code blocks are
// dropped, a link keeps only its words, a bare address goes, and bullets lose
// their mark, and abbreviations are written out (SpokenWords). A voice
// says emoji and smileys by name, or as their punctuation, so they go. Each
// line ends as a sentence (spokenLineEnds), since the break between lines is
// lost on the way to the voice. The written reply still carries all of it.
func SpeakableText(md string) string {
	var kept []string
	inCode := false
	for _, ln := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") {
			inCode = !inCode
			continue
		}
		if !inCode {
			kept = append(kept, ln)
		}
	}
	s := mdLink.ReplaceAllString(strings.Join(kept, "\n"), "$1")
	s = MarkdownToPlain(s)
	s = spokenURL.ReplaceAllString(s, "")
	s = spokenEmoji.ReplaceAllString(s, "")
	for i := 0; i < 3 && spokenSmiley.MatchString(s); i++ { // two in a row share a space
		s = spokenSmiley.ReplaceAllString(s, "$1$2")
	}
	// The page sends read-aloud a line at a time, ending in its break, which
	// MarkdownToPlain trims: put it back so that line is ended too.
	if strings.HasSuffix(strings.TrimRight(md, " \t"), "\n") {
		s += "\n"
	}
	s = spokenLineEnds(s)
	s = strings.ReplaceAll(s, "• ", "")
	s = spokenEmptyPair.ReplaceAllString(s, "")
	s = spokenSpaces.ReplaceAllString(s, " ")
	s = spokenLoosePunc.ReplaceAllString(s, "$1")
	return SpokenWords(strings.TrimSpace(s))
}

// spokenLineEnds ends each line as a sentence. A heading, a list item, or a
// line that ends in a colon ("What you'd need now:") has no stop of its own,
// and the voice gets the lines joined, so without one they run together as
// one sentence. A line ending in a colon gets a full stop in its place, one
// with no closing punctuation gets one added. A line that runs on into the
// next (a comma, a semicolon, or the line after starting lowercase) is left
// as it is, except before or in a list. So is the text's last line, with
// no break after it: the end of the text ends it.
func spokenLineEnds(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines[:len(lines)-1] {
		t := strings.TrimRight(ln, " \t")
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "• "))
		if body == "" {
			continue
		}
		// A blank line between them is a paragraph's end, never a run-on.
		next := strings.TrimSpace(lines[i+1])
		inList := strings.HasPrefix(strings.TrimSpace(t), "• ") || strings.HasPrefix(next, "• ")
		if !inList && next != "" && unicode.IsLower(firstRune(next)) {
			continue
		}
		last := lastRune(t)
		switch {
		case last == ':':
			lines[i] = strings.TrimSuffix(t, ":") + "."
		case strings.ContainsRune(".!?,;", last):
		case strings.ContainsRune(")]\"'\u201d\u2019", last) && strings.ContainsRune(".!?", lastRune(strings.TrimRight(t, ")]\"'\u201d\u2019"))):
		default:
			lines[i] = t + "."
		}
	}
	return strings.Join(lines, "\n")
}
