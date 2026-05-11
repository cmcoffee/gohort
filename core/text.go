package core

import (
	"fmt"
	"regexp"
	"strings"
)

// Precompiled regex patterns for URL/citation extraction.
//
// URL bodies allow one level of balanced parens so real-world links like
// Lancet DOIs `PIIS2542-5196(25)00054-X`, Wikipedia titles `Foo_(disambiguation)`,
// and MSDN paths don't truncate at the first inner `)`. Go's RE2 can't do
// recursive balance, so we only handle one nested pair — deeper nesting is
// rare enough to accept as a known limit.
var (
	BareURLPattern   = regexp.MustCompile(`https?://(?:[^\s\(\)\]>"]|\([^\s\)\]>"]*\))+`)
	MDLinkPattern    = regexp.MustCompile(`\[((?:[^\[\]]|\[[^\]]*\])*)\]\((https?://(?:[^\s\(\)]|\([^\s\)]*\))+)\)`)
	TaggedSrcPattern = regexp.MustCompile(`^\[(\d+)\]\s*\[((?:[^\[\]]|\[[^\]]*\])*)\]\((https?://(?:[^\(\)\n]|\([^\)\n]*\))+)\)`)
	CiteRefPattern   = regexp.MustCompile(`\[(\d+)\]`)
	DomainPattern    = regexp.MustCompile(`https?://([^\s/\)\]>"]+)`)
	// inlineLinkPattern catches markdown links to non-http targets:
	// TOC anchors `[Setup](#setup)`, relative paths `[Manual](./docs.md)`,
	// mailto links `[Mail](mailto:foo@bar)`. Runs as a second pass after
	// MDLinkPattern so external URLs already replaced are skipped (the
	// HTML they emit has no remaining `[text](url)` shape).
	// Restricts the URL part to non-paren / non-whitespace chars to
	// avoid swallowing trailing prose punctuation.
	inlineLinkPattern = regexp.MustCompile(`\[([^\]\n]+)\]\(([^\s)]+)\)`)
)

// HTMLEscape escapes special HTML characters.
func HTMLEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// HTMLUnescape reverses HTMLEscape.
func HTMLUnescape(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

// inlineHTMLPassthroughRE matches common inline-formatting HTML tags
// whose full span should survive the HTML-escape step so their tags
// render correctly instead of leaking into the page as literal text.
// Covers <sup>, <sub>, and anchor tags (<a href="...">) because the
// blogger emits clickable citation superscripts that must reach the
// browser intact. Three alternates (not one parameterized pattern)
// so each non-greedy `.*?` is scoped to the matching closer — a
// single `<(sup|sub|a)>...</(sup|sub|a)>` would stop at whichever
// closer appears first and mismatched-tag breakage returns.
var inlineHTMLPassthroughRE = regexp.MustCompile(
	`<sup\b[^>]*>.*?</sup>|<sub\b[^>]*>.*?</sub>|<a\b[^>]*>.*?</a>`)

// InlineMarkdownToHTML converts inline markdown (bold, italic, code) to HTML.
func InlineMarkdownToHTML(s string) string {
	// Protect approved inline HTML spans from the escape pass. Without
	// this the blogger's <sup><a href="...">18</a></sup> citations
	// reach the browser as escaped text — the tags show up as
	// "&lt;sup&gt;18&lt;/sup&gt;" instead of rendering. Extract each
	// protected span into a placeholder map keyed by a NUL-wrapped
	// sentinel that won't appear in real prose or in HTMLEscape's
	// output, escape the surrounding text, then substitute each
	// placeholder back with the original verbatim HTML.
	var protected []string
	s = inlineHTMLPassthroughRE.ReplaceAllStringFunc(s, func(match string) string {
		idx := len(protected)
		protected = append(protected, match)
		return fmt.Sprintf("\x00HTMLPROTECT%d\x00", idx)
	})
	s = HTMLEscape(s)
	// Auto-links — three forms:
	//   1) `<https://url>`    → CommonMark angle-bracket auto-link
	//   2) `<email@host>`    → mailto auto-link
	//   3) bare `https://url` in prose → GFM-style auto-link
	// These run BEFORE the [text](url) link parsers so a bare URL
	// inside a markdown link's text doesn't get double-wrapped.
	// The regexes look for `&lt;` and `&gt;` because HTMLEscape already
	// converted any literal `<` / `>`.
	autolinkAngle := regexp.MustCompile(`&lt;(https?://[^\s&]+)&gt;`)
	s = autolinkAngle.ReplaceAllString(s,
		`<a href="$1" target="_blank" rel="noopener noreferrer">$1</a>`)
	autolinkMailAngle := regexp.MustCompile(`&lt;([^\s@&]+@[^\s&]+)&gt;`)
	s = autolinkMailAngle.ReplaceAllString(s, `<a href="mailto:$1">$1</a>`)
	// Bare URL in prose — match http/https URLs that AREN'T already
	// inside an anchor's href or following the ]( of a markdown link.
	// Negative lookbehind isn't supported in Go's regexp engine, so
	// we use a heuristic: only match URLs preceded by whitespace,
	// a bracket open, or a punctuation-like char that ends a sentence.
	// (?:^|[\s>(]) — start of string OR a context that's safe.
	bareURL := regexp.MustCompile(`(^|[\s>(])(https?://[^\s<>"'()]+)`)
	s = bareURL.ReplaceAllString(s,
		`$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>`)

	// Markdown links: [text](url) → <a href="url">text</a>.
	// Two passes:
	//   1) MDLinkPattern handles http/https URLs with one level of
	//      balanced parens (Wikipedia disambiguation paths, MSDN
	//      docs, Lancet DOIs). target="_blank" so external links open
	//      in a new tab without navigating the reader off the page.
	//   2) inlineLinkPattern catches everything else — TOC anchors
	//      ([Foo](#foo)), relative paths, mailto:, and crucially
	//      bare-domain URLs like [Github](github.com/foo) where the
	//      LLM forgot the scheme. The replacement function classifies
	//      each URL and:
	//        - leaves #anchors and /paths alone (no target=_blank)
	//        - leaves mailto: alone
	//        - prepends https:// to bare domains and adds target=_blank
	//      Without classification, bare domains rendered as relative
	//      paths resolved against the page URL — broken links.
	s = MDLinkPattern.ReplaceAllString(s,
		`<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>`)
	s = inlineLinkPattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := inlineLinkPattern.FindStringSubmatch(match)
		if sub == nil {
			return match
		}
		text, url := sub[1], sub[2]
		switch {
		case strings.HasPrefix(url, "#"):
			// Internal anchor — stays in-document.
			return fmt.Sprintf(`<a href="%s">%s</a>`, url, text)
		case strings.HasPrefix(url, "/"):
			// Relative path — leave as-is, same document context.
			return fmt.Sprintf(`<a href="%s">%s</a>`, url, text)
		case strings.HasPrefix(url, "mailto:"), strings.HasPrefix(url, "tel:"):
			return fmt.Sprintf(`<a href="%s">%s</a>`, url, text)
		case strings.Contains(url, "."):
			// Bare-domain or scheme-less URL — promote to https://
			// and treat as external (new tab).
			return fmt.Sprintf(`<a href="https://%s" target="_blank" rel="noopener noreferrer">%s</a>`, url, text)
		default:
			// Unclassifiable — leave verbatim so we don't make it worse.
			return fmt.Sprintf(`<a href="%s">%s</a>`, url, text)
		}
	})
	// Bold.
	for strings.Contains(s, "**") {
		start := strings.Index(s, "**")
		end := strings.Index(s[start+2:], "**")
		if end < 0 { break }
		end += start + 2
		s = s[:start] + "<strong>" + s[start+2:end] + "</strong>" + s[end+2:]
	}
	// Inline code.
	for strings.Contains(s, "`") {
		start := strings.Index(s, "`")
		end := strings.Index(s[start+1:], "`")
		if end < 0 { break }
		end += start + 1
		s = s[:start] + "<code>" + s[start+1:end] + "</code>" + s[end+1:]
	}
	// Italic (after bold to avoid conflicts).
	for strings.Contains(s, "*") {
		start := strings.Index(s, "*")
		end := strings.Index(s[start+1:], "*")
		if end < 0 { break }
		end += start + 1
		s = s[:start] + "<em>" + s[start+1:end] + "</em>" + s[end+1:]
	}
	// Restore protected HTML spans last — the markdown link loop
	// ran on placeholder-substituted text, so the [brackets] inside
	// the restored <sup> spans can't interact with real markdown.
	for idx, raw := range protected {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00HTMLPROTECT%d\x00", idx), raw)
	}
	return s
}

// HeadingAnchor turns a heading's text into a slug matching GitHub /
// GFM exactly (the algorithm every LLM has internalized from training):
//   - lowercase
//   - replace each space with a dash
//   - drop every character that is not [a-z0-9-_]
//   - do NOT collapse consecutive dashes
//   - do NOT trim leading/trailing dashes
//
// Crucially, runs of dashes ARE preserved — "Distribution & Version"
// becomes "distribution--version" (space-amp-space → dash-drop-dash).
// Earlier we collapsed dashes, which produced "distribution-version"
// while the LLM wrote "distribution--version", and every link broke.
//
// Inline markdown (bold/italic/code/link) is stripped before
// slugifying so `## **Setup** Steps` matches `[Setup Steps](#setup-steps)`.
func HeadingAnchor(s string) string {
	s = regexp.MustCompile(`\[([^\]]*)\]\([^\)]*\)`).ReplaceAllString(s, "$1")
	s = regexp.MustCompile("[`*]").ReplaceAllString(s, "")
	s = strings.ToLower(s)
	var out strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out.WriteRune(r)
		case r == ' ':
			out.WriteRune('-')
		}
	}
	return out.String()
}

// scanHeadingSlugs walks the input markdown once and assigns each
// heading the canonical GitHub-flavored slug (lowercase, alphanumerics
// preserved, all other chars become dashes, consecutive dashes
// collapsed, leading/trailing dashes trimmed). Duplicates get a
// "-N" disambiguator in document order — same as GitHub.
//
// This is the slug algorithm the LLM is told to use in the system
// prompt, so a `[Section](#slug)` link the LLM writes lands on the
// correctly-slugged heading without any post-process matching layer.
type headingSlug struct {
	Text string // cleaned heading text (no inline markdown)
	Slug string // GitHub-style slug
}

func scanHeadingSlugs(md string) []headingSlug {
	stripInline := regexp.MustCompile("[`*]")
	stripLink := regexp.MustCompile(`\[([^\]]*)\]\([^\)]*\)`)
	used := map[string]int{}
	var out []headingSlug
	in_code := false
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		// Track fenced code blocks. The renderer skips heading parsing
		// inside code blocks, so we must too — otherwise a `## foo` line
		// inside a ``` block adds a phantom slug that the render loop
		// never pops, shifting every later heading's id by one and
		// breaking every TOC link past that point.
		if strings.HasPrefix(line, "```") {
			in_code = !in_code
			continue
		}
		if in_code {
			continue
		}
		var raw string
		if strings.HasPrefix(line, "### ") {
			raw = strings.TrimSpace(line[4:])
		} else if strings.HasPrefix(line, "## ") {
			raw = strings.TrimSpace(line[3:])
		} else if strings.HasPrefix(line, "# ") {
			raw = strings.TrimSpace(line[2:])
		} else {
			continue
		}
		clean := stripLink.ReplaceAllString(raw, "$1")
		clean = stripInline.ReplaceAllString(clean, "")
		clean = strings.TrimSpace(clean)
		base := HeadingAnchor(clean)
		slug := base
		if n := used[base]; n > 0 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		used[base]++
		out = append(out, headingSlug{Text: clean, Slug: slug})
	}
	return out
}

// resolveReferenceLinks normalizes reference-style links to the
// inline `[text](url)` form so the rest of the renderer can process
// them with one code path.
//
// Reference definitions look like:    [label]: https://url
// Reference uses look like:            [text][label]   or   [text][]
//
// The shortcut form `[text][]` reuses `text` as the label. Both
// label keys are case-insensitive per CommonMark. Definition lines
// are removed from the body so they don't render as literal text.
//
// Returns the transformed markdown. When the document contains no
// reference definitions, the original is returned unchanged.
func resolveReferenceLinks(md string) string {
	refDef := regexp.MustCompile(`(?m)^[ \t]*\[([^\]]+)\]:[ \t]+(\S+)(?:[ \t]+"[^"]*")?[ \t]*$`)
	refs := map[string]string{}
	md = refDef.ReplaceAllStringFunc(md, func(m string) string {
		sub := refDef.FindStringSubmatch(m)
		if sub != nil {
			refs[strings.ToLower(strings.TrimSpace(sub[1]))] = sub[2]
		}
		return "" // drop the definition line
	})
	if len(refs) == 0 {
		return md
	}
	// Match [text][label] and [text][] (shortcut). Label is empty or
	// a non-empty string of non-bracket chars; we look it up in refs
	// and rewrite to inline-link form for downstream processing.
	refUse := regexp.MustCompile(`\[([^\]\n]+)\]\[([^\]\n]*)\]`)
	md = refUse.ReplaceAllStringFunc(md, func(m string) string {
		sub := refUse.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		text, label := sub[1], strings.TrimSpace(sub[2])
		if label == "" {
			label = text
		}
		if url, ok := refs[strings.ToLower(label)]; ok {
			return "[" + text + "](" + url + ")"
		}
		return m // keep original; unresolved labels render as literal text
	})
	// Collapse extra blank lines left behind by stripped definitions.
	md = regexp.MustCompile(`\n{3,}`).ReplaceAllString(md, "\n\n")
	return md
}

// MarkdownToHTML converts markdown text to HTML.
func MarkdownToHTML(md string) string {
	md = resolveReferenceLinks(md)
	headings := scanHeadingSlugs(md)
	headingIdx := 0
	var out strings.Builder
	lines := strings.Split(md, "\n")
	in_code := false
	in_list := false
	in_ol := false
	in_table := false

	// nextHeadingSlug pops a slug from the pre-scanned list. The
	// pre-scan order is the same as the render order, so popping in
	// order keeps slugs aligned with their headings.
	nextHeadingSlug := func() string {
		if headingIdx < len(headings) {
			s := headings[headingIdx].Slug
			headingIdx++
			return s
		}
		return ""
	}

	closeBlocks := func() {
		if in_list { out.WriteString("</ul>\n"); in_list = false }
		if in_ol { out.WriteString("</ol>\n"); in_ol = false }
		if in_table { out.WriteString("</tbody></table>\n"); in_table = false }
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Code blocks.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in_code {
				out.WriteString("</code></pre>\n")
				in_code = false
			} else {
				closeBlocks()
				out.WriteString("<pre><code>")
				in_code = true
			}
			continue
		}
		if in_code {
			// Don't add newline before the first line — prevents leading blank line.
			if !strings.HasSuffix(out.String(), "<pre><code>") {
				out.WriteString("\n")
			}
			out.WriteString(HTMLEscape(line))
			continue
		}

		stripped := strings.TrimSpace(line)

		// Tables.
		if strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|") {
			// Separator row (e.g. |---|---|) — marks end of header, skip.
			isSep := true
			for _, ch := range strings.Trim(stripped, "|") {
				if ch != '-' && ch != ':' && ch != ' ' && ch != '|' {
					isSep = false
					break
				}
			}
			if isSep {
				continue
			}
			cells := strings.Split(strings.Trim(stripped, "|"), "|")
			if !in_table {
				if in_list { out.WriteString("</ul>\n"); in_list = false }
				if in_ol { out.WriteString("</ol>\n"); in_ol = false }
				out.WriteString("<table><thead><tr>")
				for _, cell := range cells {
					fmt.Fprintf(&out, "<th>%s</th>", InlineMarkdownToHTML(strings.TrimSpace(cell)))
				}
				out.WriteString("</tr></thead><tbody>\n")
				in_table = true
			} else {
				out.WriteString("<tr>")
				for _, cell := range cells {
					fmt.Fprintf(&out, "<td>%s</td>", InlineMarkdownToHTML(strings.TrimSpace(cell)))
				}
				out.WriteString("</tr>\n")
			}
			continue
		}
		if in_table {
			out.WriteString("</tbody></table>\n")
			in_table = false
		}

		if stripped == "" {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			continue
		}

		// Headers — IDs come from the pre-scan slug list so headings
		// and TOC links share a stable, predictable mapping.
		if strings.HasPrefix(stripped, "### ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			text := stripped[4:]
			fmt.Fprintf(&out, "<h3 id=%q>%s</h3>\n", nextHeadingSlug(), InlineMarkdownToHTML(text))
			continue
		}
		if strings.HasPrefix(stripped, "## ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			text := stripped[3:]
			fmt.Fprintf(&out, "<h2 id=%q>%s</h2>\n", nextHeadingSlug(), InlineMarkdownToHTML(text))
			continue
		}
		if strings.HasPrefix(stripped, "# ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			text := stripped[2:]
			fmt.Fprintf(&out, "<h1 id=%q>%s</h1>\n", nextHeadingSlug(), InlineMarkdownToHTML(text))
			continue
		}

		// Horizontal rules.
		if stripped == "---" || stripped == "***" || stripped == "___" {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			out.WriteString("<hr>\n")
			continue
		}

		// Bullet lists.
		if strings.HasPrefix(stripped, "- ") || strings.HasPrefix(stripped, "* ") {
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			if !in_list { out.WriteString("<ul>\n"); in_list = true }
			fmt.Fprintf(&out, "<li>%s</li>\n", InlineMarkdownToHTML(stripped[2:]))
			continue
		}

		// Numbered lists.
		if len(stripped) > 2 && stripped[0] >= '0' && stripped[0] <= '9' && strings.Contains(stripped[:min(5, len(stripped))], ". ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if !in_ol { out.WriteString("<ol>\n"); in_ol = true }
			dot := strings.Index(stripped, ". ")
			fmt.Fprintf(&out, "<li>%s</li>\n", InlineMarkdownToHTML(stripped[dot+2:]))
			continue
		}

		// Blockquote.
		if strings.HasPrefix(stripped, "> ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			fmt.Fprintf(&out, "<blockquote>%s</blockquote>\n", InlineMarkdownToHTML(stripped[2:]))
			continue
		}

		if in_list { out.WriteString("</ul>\n"); in_list = false }
		if in_ol { out.WriteString("</ol>\n"); in_ol = false }
		fmt.Fprintf(&out, "<p>%s</p>\n", InlineMarkdownToHTML(stripped))
	}

	if in_code { out.WriteString("</code></pre>\n") }
	if in_list { out.WriteString("</ul>\n") }
	if in_ol { out.WriteString("</ol>\n") }
	if in_table { out.WriteString("</tbody></table>\n") }
	return out.String()
}

// HTMLToMarkdown does a rough conversion of HTML back to markdown.
func HTMLToMarkdown(html string) string {
	s := html
	// Code blocks.
	s = regexp.MustCompile(`<pre><code>([\s\S]*?)</code></pre>`).ReplaceAllStringFunc(s, func(m string) string {
		inner := regexp.MustCompile(`<pre><code>([\s\S]*?)</code></pre>`).FindStringSubmatch(m)
		if inner == nil { return m }
		return "\n```\n" + strings.TrimSpace(HTMLUnescape(inner[1])) + "\n```\n"
	})
	// Headers.
	s = regexp.MustCompile(`<h1>(.*?)</h1>`).ReplaceAllString(s, "# $1\n")
	s = regexp.MustCompile(`<h2>(.*?)</h2>`).ReplaceAllString(s, "## $1\n")
	s = regexp.MustCompile(`<h3>(.*?)</h3>`).ReplaceAllString(s, "### $1\n")
	// Bold/italic/code.
	s = regexp.MustCompile(`<strong>(.*?)</strong>`).ReplaceAllString(s, "**$1**")
	s = regexp.MustCompile(`<em>(.*?)</em>`).ReplaceAllString(s, "*$1*")
	s = regexp.MustCompile(`<code>(.*?)</code>`).ReplaceAllStringFunc(s, func(m string) string {
		inner := regexp.MustCompile(`<code>(.*?)</code>`).FindStringSubmatch(m)
		if inner == nil { return m }
		return "`" + HTMLUnescape(inner[1]) + "`"
	})
	// Lists.
	s = regexp.MustCompile(`<li>(.*?)</li>`).ReplaceAllString(s, "- $1\n")
	s = regexp.MustCompile(`</?[ou]l>`).ReplaceAllString(s, "")
	// Blockquotes.
	s = regexp.MustCompile(`<blockquote>(.*?)</blockquote>`).ReplaceAllString(s, "> $1\n")
	// Paragraphs and breaks.
	s = regexp.MustCompile(`<p>(.*?)</p>`).ReplaceAllString(s, "$1\n\n")
	s = regexp.MustCompile(`<br\s*/?>`).ReplaceAllString(s, "\n")
	// Strip remaining tags.
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	s = HTMLUnescape(s)
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// ExtractURLs returns all unique URLs found in text.
func ExtractURLs(text string) []string {
	seen := make(map[string]bool)
	var urls []string
	for _, u := range BareURLPattern.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:)")
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	return urls
}

// ExtractDomains returns deduplicated URL domains from text.
func ExtractDomains(text string) map[string]bool {
	domains := make(map[string]bool)
	for _, m := range DomainPattern.FindAllStringSubmatch(text, -1) {
		domains[m[1]] = true
	}
	return domains
}

// CountURLs counts unique URLs in text.
func CountURLs(text string) int {
	return len(ExtractURLs(text))
}

// SplitSourcesSection splits a report at the "\n## Sources" header.
// Returns the body (everything before the header, trimmed) and the sources
// section (the header and everything after, including the leading newline).
// If no Sources section exists, returns (text, "").
func SplitSourcesSection(text string) (body string, sources string) {
	if idx := strings.Index(text, "\n## Sources"); idx >= 0 {
		return strings.TrimSpace(text[:idx]), text[idx:]
	}
	return text, ""
}

// StripSourcesSection returns text with any "\n## Sources..." section
// removed. Equivalent to SplitSourcesSection's first return value when
// you don't need to preserve the sources block.
func StripSourcesSection(text string) string {
	body, _ := SplitSourcesSection(text)
	return body
}

// StripSection removes any "\n## <name>..." section up through the next
// "\n## " header (or to end-of-string if it's the last section). Useful
// for cleaning up LLM-generated sections that should be regenerated in
// code (Sources, Confidence Level, Fact Check Notes, etc.).
func StripSection(text, name string) string {
	header := "\n## " + name
	idx := strings.Index(text, header)
	if idx < 0 {
		return text
	}
	rest := text[idx+len(header):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		return text[:idx] + rest[next:]
	}
	return text[:idx]
}

// StripUncitedSources removes source lines from the ## Sources section
// that are not referenced anywhere in the report body.
func StripUncitedSources(report string) string {
	idx := strings.Index(report, "\n## Sources")
	if idx < 0 {
		return report
	}
	body := report[:idx]
	sourcesSection := report[idx:]

	// Find all [N] references in the body, including comma-separated [N, N, N].
	cited := make(map[string]bool)
	for _, m := range CiteRefPattern.FindAllString(body, -1) {
		cited[m] = true
	}
	// Also handle [N, N, N] format.
	multiCite := regexp.MustCompile(`\[[\d,\s]+\]`)
	for _, m := range multiCite.FindAllString(body, -1) {
		for _, n := range regexp.MustCompile(`\d+`).FindAllString(m, -1) {
			cited["["+n+"]"] = true
		}
	}

	// Filter source lines — keep only cited ones.
	var filtered strings.Builder
	for _, line := range strings.Split(sourcesSection, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := CiteRefPattern.FindString(trimmed); m != "" && strings.HasPrefix(trimmed, m) {
			// Drop if uncited.
			if !cited[m] {
				continue
			}
			// Drop if no URL -- not a real source.
			if !strings.Contains(trimmed, "http://") && !strings.Contains(trimmed, "https://") {
				continue
			}
		}
		filtered.WriteString(line)
		filtered.WriteString("\n")
	}

	return body + strings.TrimRight(filtered.String(), "\n")
}

// StripInternalLabels removes bracketed internal labels from a report,
// keeping only numeric source citations like [1], [2, 3].
func StripInternalLabels(report string) string {
	// Match [anything that contains letters] but not [N] or [N, N, N].
	labelPattern := regexp.MustCompile(`\[[^\]]*[a-zA-Z][^\]]*\]`)
	report = labelPattern.ReplaceAllStringFunc(report, func(match string) string {
		inner := match[1 : len(match)-1]
		// Keep if it's just numbers and commas/spaces (source citations).
		onlyNumeric := true
		for _, c := range inner {
			if c != ' ' && c != ',' && (c < '0' || c > '9') {
				onlyNumeric = false
				break
			}
		}
		if onlyNumeric {
			return match
		}
		return ""
	})
	// Clean up residue from stripped tier tags like "[blog], [social], [gov]"
	// that appeared inline. Without this, strips leave patterns like "(, , )"
	// or ", , or evidence" visible in the final prose.
	emptyParens := regexp.MustCompile(`\(\s*(?:,\s*)+\)`)
	report = emptyParens.ReplaceAllString(report, "")
	commaRuns := regexp.MustCompile(`(?:,\s*){2,}`)
	report = commaRuns.ReplaceAllString(report, ", ")
	orphanLeading := regexp.MustCompile(`(?m)(\s),\s*(and|or)\s`)
	report = orphanLeading.ReplaceAllString(report, "$1$2 ")
	return report
}

// MarkdownToConfluence converts markdown to Confluence storage format (XHTML).
// Code blocks become ac:structured-macro code blocks. Tables, blockquotes,
// and icon comments are converted to Confluence macros. Output can be pasted
// into Confluence's source editor.
func MarkdownToConfluence(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	in_code := false
	code_lang := ""
	in_list := false
	in_ol := false
	in_table := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Code blocks.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in_code {
				out.WriteString("]]></ac:plain-text-body></ac:structured-macro>\n")
				in_code = false
				code_lang = ""
			} else {
				if in_list { out.WriteString("</ul>\n"); in_list = false }
				if in_ol { out.WriteString("</ol>\n"); in_ol = false }
				if in_table { out.WriteString("</tbody></table>\n"); in_table = false }
				code_lang = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "```"))
				if code_lang == "" {
					code_lang = "none"
				}
				fmt.Fprintf(&out, "<ac:structured-macro ac:name=\"code\"><ac:parameter ac:name=\"language\">%s</ac:parameter><ac:parameter ac:name=\"theme\">Midnight</ac:parameter><ac:plain-text-body><![CDATA[", code_lang)
				in_code = true
			}
			continue
		}
		if in_code {
			if !strings.HasSuffix(out.String(), "<![CDATA[") {
				out.WriteString("\n")
			}
			out.WriteString(line)
			continue
		}

		stripped := strings.TrimSpace(line)

		// Tables.
		if strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|") {
			if strings.Contains(stripped, "---") {
				continue
			}
			cells := strings.Split(strings.Trim(stripped, "|"), "|")
			if !in_table {
				if in_list { out.WriteString("</ul>\n"); in_list = false }
				if in_ol { out.WriteString("</ol>\n"); in_ol = false }
				out.WriteString("<table><tbody><tr>")
				for _, cell := range cells {
					fmt.Fprintf(&out, "<th>%s</th>", InlineMarkdownToHTML(strings.TrimSpace(cell)))
				}
				out.WriteString("</tr>\n")
				in_table = true
			} else {
				out.WriteString("<tr>")
				for _, cell := range cells {
					fmt.Fprintf(&out, "<td>%s</td>", InlineMarkdownToHTML(strings.TrimSpace(cell)))
				}
				out.WriteString("</tr>\n")
			}
			continue
		}
		if in_table {
			out.WriteString("</tbody></table>\n")
			in_table = false
		}

		if stripped == "" {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			continue
		}

		// Headers.
		if strings.HasPrefix(stripped, "### ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			fmt.Fprintf(&out, "<h3>%s</h3>\n", InlineMarkdownToHTML(stripped[4:]))
			continue
		}
		if strings.HasPrefix(stripped, "## ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			fmt.Fprintf(&out, "<h2>%s</h2>\n", InlineMarkdownToHTML(stripped[3:]))
			continue
		}
		if strings.HasPrefix(stripped, "# ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			fmt.Fprintf(&out, "<h1>%s</h1>\n", InlineMarkdownToHTML(stripped[2:]))
			continue
		}

		// Icon comments → Confluence info/warning/note/tip panels.
		if strings.HasPrefix(stripped, "<!-- icon:") {
			icon := strings.TrimSuffix(strings.TrimPrefix(stripped, "<!-- icon:"), "-->")
			icon = strings.TrimSpace(icon)
			macro := "info"
			if strings.Contains(icon, "warning") || strings.Contains(icon, "caution") {
				macro = "warning"
			} else if strings.Contains(icon, "note") || strings.Contains(icon, "important") {
				macro = "note"
			} else if strings.Contains(icon, "tip") || strings.Contains(icon, "success") || strings.Contains(icon, "check") {
				macro = "tip"
			}
			var body strings.Builder
			for i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if next == "" || strings.HasPrefix(next, "#") || strings.HasPrefix(next, "<!-- icon:") {
					break
				}
				i++
				fmt.Fprintf(&body, "<p>%s</p>", InlineMarkdownToHTML(next))
			}
			fmt.Fprintf(&out, "<ac:structured-macro ac:name=\"%s\"><ac:rich-text-body>%s</ac:rich-text-body></ac:structured-macro>\n", macro, body.String())
			continue
		}

		// Blockquotes → Confluence info panel.
		if strings.HasPrefix(stripped, "> ") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			var body strings.Builder
			fmt.Fprintf(&body, "<p>%s</p>", InlineMarkdownToHTML(stripped[2:]))
			for i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "> ") {
				i++
				fmt.Fprintf(&body, "<p>%s</p>", InlineMarkdownToHTML(strings.TrimSpace(lines[i])[2:]))
			}
			fmt.Fprintf(&out, "<ac:structured-macro ac:name=\"info\"><ac:rich-text-body>%s</ac:rich-text-body></ac:structured-macro>\n", body.String())
			continue
		}

		// Bullet lists.
		if strings.HasPrefix(stripped, "- ") || strings.HasPrefix(stripped, "* ") {
			if in_ol { out.WriteString("</ol>\n"); in_ol = false }
			if !in_list { out.WriteString("<ul>\n"); in_list = true }
			fmt.Fprintf(&out, "<li>%s</li>\n", InlineMarkdownToHTML(stripped[2:]))
			continue
		}

		// Numbered lists.
		if len(stripped) > 2 && stripped[0] >= '0' && stripped[0] <= '9' && strings.Contains(stripped[:3], ".") {
			if in_list { out.WriteString("</ul>\n"); in_list = false }
			if !in_ol { out.WriteString("<ol>\n"); in_ol = true }
			dot := strings.Index(stripped, ".")
			fmt.Fprintf(&out, "<li>%s</li>\n", InlineMarkdownToHTML(strings.TrimSpace(stripped[dot+1:])))
			continue
		}

		// Paragraph.
		if in_list { out.WriteString("</ul>\n"); in_list = false }
		if in_ol { out.WriteString("</ol>\n"); in_ol = false }
		fmt.Fprintf(&out, "<p>%s</p>\n", InlineMarkdownToHTML(stripped))
	}

	if in_code { out.WriteString("]]></ac:plain-text-body></ac:structured-macro>\n") }
	if in_list { out.WriteString("</ul>\n") }
	if in_ol { out.WriteString("</ol>\n") }
	if in_table { out.WriteString("</tbody></table>\n") }

	return out.String()
}
