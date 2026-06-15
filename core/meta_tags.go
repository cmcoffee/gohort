package core

import (
	"regexp"
	"strings"
)

// Generic internal meta marker — reserved for framework/agent-internal
// directives that must NEVER reach the user. Anything an agent wraps in
// <gohort-meta>…</gohort-meta> is stripped from the final reply, so a leaked
// internal note can't be mistaken for content. The XML-style, namespaced
// element name is collision-proof (it will never appear in real prose or
// markdown). Case-insensitive; non-greedy; matches inline or multi-line.
var (
	metaTagRe = regexp.MustCompile(`(?is)<gohort-meta>.*?</gohort-meta>`)

	// Known framework delivery markers. These are normally CONSUMED (the file
	// is attached) and stripped by the surface that handles them; this is the
	// safety net for ones that leak unconsumed into user-facing text — the
	// motivating case ([ATTACH: funny-meme.png] showing up verbatim).
	leakedAttachRe      = regexp.MustCompile(`\[ATTACH:\s*[^\]]*\]`)
	leakedShellAttachRe = regexp.MustCompile(`(?is)<<<ATTACH:[^>]*>>>.*?<<<END>>>`)

	// Tidy up the blank space a removed marker leaves behind: collapse 3+
	// newlines to 2, and trim trailing spaces on a line.
	metaExtraBlankRe = regexp.MustCompile(`\n{3,}`)
	metaTrailingWSRe = regexp.MustCompile(`[ \t]+\n`)
)

// StripMetaTags removes framework-internal markers from a final, user-facing
// reply: the reserved <gohort-meta>…</gohort-meta> convention plus known
// delivery markers ([ATTACH: …], <<<ATTACH:…>>>…<<<END>>>) that leak when
// unconsumed. Safe to call on any reply — a fast no-op when none are present.
//
// IMPORTANT: call this at the FINAL output boundary, AFTER any surface that
// legitimately consumes a marker (e.g. phantom's applyAttachMarkers attaches
// the file) has run — otherwise it would strip the marker before it's acted on.
func StripMetaTags(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "<gohort-meta") && !strings.Contains(s, "[ATTACH") && !strings.Contains(s, "<<<ATTACH") {
		return s
	}
	s = metaTagRe.ReplaceAllString(s, "")
	s = leakedAttachRe.ReplaceAllString(s, "")
	s = leakedShellAttachRe.ReplaceAllString(s, "")
	s = metaTrailingWSRe.ReplaceAllString(s, "\n")
	s = metaExtraBlankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
