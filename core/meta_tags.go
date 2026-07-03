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
	leakedAttachRe = regexp.MustCompile(`\[ATTACH:\s*[^\]]*\]`)
	// The canonical shell marker (see tools/temptool: <<<ATTACH:mime … ATTACH_END>>>)
	// closes with ATTACH_END>>>. Match that AND the older <<<END>>> form some
	// prompts/tools used, lazily to the first close, so a leaked marker in either
	// shape never dumps raw base64 into the user-facing text.
	leakedShellAttachRe = regexp.MustCompile(`(?is)<<<ATTACH:.*?(?:ATTACH_END>>>|<<<END>>>)`)

	// Tidy up the blank space a removed marker leaves behind: collapse 3+
	// newlines to 2, and trim trailing spaces on a line.
	metaExtraBlankRe = regexp.MustCompile(`\n{3,}`)
	metaTrailingWSRe = regexp.MustCompile(`[ \t]+\n`)

	// Leaked tool-call / tool-code markup — the XML-ish shapes models emit when
	// invoking tools (<tool_call>…</tool_call>, <tool_code>…</tool_code>,
	// <function=name>…</function>). Normally parsed and consumed by the agent
	// loop; this strips any that leak verbatim into saved content. Balanced blocks
	// first, then a sweep for orphan/self-closing openers and stray closers.
	toolCallBlockRe = regexp.MustCompile(`(?is)<tool_call>.*?</tool_call>`)
	toolCodeBlockRe = regexp.MustCompile(`(?is)<tool_code>.*?</tool_code>`)
	functionBlockRe = regexp.MustCompile(`(?is)<function[= ][^>]*>.*?</function>`)
	strayToolTagRe  = regexp.MustCompile(`(?is)</?(?:tool_call|tool_code|function)\b[^>]*>`)
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

// StripToolCallTags removes leaked tool-call / tool-code / function-call markup
// from a content string — the XML-ish tags a model emits to invoke a tool that
// should have been parsed and consumed by the agent loop, not saved as prose.
// Handles balanced blocks plus orphan/self-closing openers and stray closers.
// Fast no-op when none are present. Complements StripThinkTags (reasoning
// delimiters) and StripMetaTags (framework markers).
func StripToolCallTags(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(s, "<tool_call") && !strings.Contains(s, "</tool_call") &&
		!strings.Contains(s, "<tool_code") && !strings.Contains(s, "</tool_code") &&
		!strings.Contains(s, "<function") && !strings.Contains(s, "</function") {
		return s
	}
	s = toolCallBlockRe.ReplaceAllString(s, "")
	s = toolCodeBlockRe.ReplaceAllString(s, "")
	s = functionBlockRe.ReplaceAllString(s, "")
	s = strayToolTagRe.ReplaceAllString(s, "")
	s = metaTrailingWSRe.ReplaceAllString(s, "\n")
	s = metaExtraBlankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
