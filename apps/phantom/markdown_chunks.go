// Markdown → plain-text multi-SMS converter. Replaces the prior
// wrap-LLM compression for async dispatch results — instead of
// summarizing a long report into one short text, we strip the
// markdown formatting (preserving every word) and split the result
// into SMS-friendly chunks. The user reads the full report as a
// sequence of texts rather than a compressed summary, so no detail
// is lost.
//
// Trade-off: persona voice is partially diluted (the wrap LLM was
// reshaping in the persona's tone; this converter is mechanical).
// But fidelity preservation matters more for research-style
// dispatches — the user wants the data, not the color.

package phantom

import (
	"fmt"
	"regexp"
	"strings"
)

// SplitMarkdownForDelivery converts a markdown report into one or
// more plain-text chunks suitable for SMS delivery. chunkSize is
// the soft target (chunks may run slightly over if the nearest
// boundary is past it; never under unless that's the whole
// content). When the result is one chunk, no numbering prefix is
// added; multiple chunks get "(i/N) " prefix so the user can
// follow ordering when their SMS client groups them.
func SplitMarkdownForDelivery(md string, chunkSize int) []string {
	plain := markdownToPlain(md)
	chunks := chunkPlainText(plain, chunkSize)
	if len(chunks) <= 1 {
		return chunks
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = fmt.Sprintf("(%d/%d) %s", i+1, len(chunks), c)
	}
	return out
}

// --- markdown stripping -------------------------------------------------

var (
	// Inline patterns. Run on each line.
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`) // [text](url) → "text (url)"
	reBold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)         // **x** → x
	reItalic1  = regexp.MustCompile(`\*([^*]+)\*`)             // *x* → x
	reItalic2  = regexp.MustCompile(`_([^_]+)_`)               // _x_ → x
	reInlineCo = regexp.MustCompile("`([^`]+)`")               // `x` → x
	reHeader   = regexp.MustCompile(`^#{1,6}\s+(.+)$`)         // # Title → Title
	reBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.+)$`)     // - item / * item → • item
	reNumbered = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.+)$`)   // 1. item → 1. item (kept)
	reQuote    = regexp.MustCompile(`^>\s*(.*)$`)              // > x → x
	reHR       = regexp.MustCompile(`^(\s*)([-*_]\s*){3,}\s*$`) // --- → blank
)

// markdownToPlain strips formatting while preserving every word of
// content. Headers retain their text + surrounding blank lines.
// Code fences strip the ```, preserve the body. Tables are kept
// roughly intact (a pretty-print pass would be nicer but most chat
// dispatches don't return tables). Block quotes lose the `>` prefix.
func markdownToPlain(md string) string {
	var out strings.Builder
	inCode := false
	lines := strings.Split(md, "\n")
	for i, ln := range lines {
		_ = i
		// Code fence boundary — strip the line; toggle the in-code
		// flag so inline-strippers don't munge the content inside.
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
		// Headers — keep the text, drop the # markers. Add a blank
		// line above so the title visually separates.
		if m := reHeader.FindStringSubmatch(ln); m != nil {
			if out.Len() > 0 {
				// Ensure exactly one blank line before the header.
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
		if reHR.MatchString(ln) {
			out.WriteByte('\n')
			continue
		}
		// Block quote prefix → drop.
		if m := reQuote.FindStringSubmatch(ln); m != nil {
			ln = m[1]
		}
		// Bullets → "• " for readability.
		if m := reBullet.FindStringSubmatch(ln); m != nil {
			ln = m[1] + "• " + m[2]
		}
		// Numbered lists stay as-is (1. item is already readable).
		// Inline replacements.
		ln = reLink.ReplaceAllString(ln, "$1 ($2)")
		ln = reBold.ReplaceAllString(ln, "$1")
		ln = reItalic1.ReplaceAllString(ln, "$1")
		ln = reItalic2.ReplaceAllString(ln, "$1")
		ln = reInlineCo.ReplaceAllString(ln, "$1")
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

// --- chunker --------------------------------------------------------------

// chunkPlainText splits plain text into chunks of approximately
// `chunkSize` characters each. Splits at the nearest paragraph
// boundary above the chunk size; falls back to sentence boundary,
// then to hard cut at chunkSize. Empty input → single empty chunk
// (callers should check before delivery).
func chunkPlainText(text string, chunkSize int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if chunkSize <= 0 {
		chunkSize = 1500
	}
	if len(text) <= chunkSize {
		return []string{text}
	}
	var chunks []string
	remaining := text
	for len(remaining) > chunkSize {
		// Find the best split point in [0, chunkSize+buffer]. Prefer
		// paragraph boundary (\n\n), then sentence boundary, then
		// hard cut at chunkSize.
		search := remaining
		if len(search) > chunkSize+200 {
			search = search[:chunkSize+200]
		}
		cut := -1
		// Paragraph boundary first.
		if p := strings.LastIndex(search[:min(len(search), chunkSize)], "\n\n"); p > chunkSize/2 {
			cut = p + 2 // include the blank line
		}
		// Sentence boundary fallback.
		if cut < 0 {
			for _, marker := range []string{". ", ".\n", "! ", "? "} {
				if p := strings.LastIndex(search[:min(len(search), chunkSize)], marker); p > chunkSize/2 {
					cut = p + len(marker)
					break
				}
			}
		}
		// Hard cut as last resort.
		if cut < 0 {
			cut = chunkSize
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimLeft(remaining[cut:], " \t\n")
	}
	if strings.TrimSpace(remaining) != "" {
		chunks = append(chunks, strings.TrimSpace(remaining))
	}
	return chunks
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
