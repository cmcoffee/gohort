// Markdown → plain-text flattening, shared by any transport that delivers to a
// surface which renders no markdown (iMessage, SMS). Lifted from phantom's
// markdown_chunks.go so bridges can reuse it without depending on the retiring
// phantom app. Strips formatting while preserving every word of content.

package core

import (
	"regexp"
	"strings"
)

var (
	// Inline patterns. Run on each line.
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)  // [text](url) → "text (url)"
	mdBold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)          // **x** → x
	mdItalic1  = regexp.MustCompile(`\*([^*]+)\*`)              // *x* → x
	mdItalic2  = regexp.MustCompile(`_([^_]+)_`)                // _x_ → x
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
		ln = mdItalic2.ReplaceAllString(ln, "$1")
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
