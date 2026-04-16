package core

import (
	"fmt"
	"regexp"
	"strings"
)

// Precompiled regex patterns for URL/citation extraction.
var (
	BareURLPattern  = regexp.MustCompile(`https?://[^\s\)\]>"]+`)
	MDLinkPattern   = regexp.MustCompile(`\[([^\]]*)\]\((https?://[^\s\)]+)\)`)
	TaggedSrcPattern = regexp.MustCompile(`^\[(\d+)\]\s*\[([^\]]*)\]\((https?://[^\)]+)\)`)
	CiteRefPattern  = regexp.MustCompile(`\[(\d+)\]`)
	DomainPattern   = regexp.MustCompile(`https?://([^\s/\)\]>"]+)`)
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

// InlineMarkdownToHTML converts inline markdown (bold, italic, code) to HTML.
func InlineMarkdownToHTML(s string) string {
	s = HTMLEscape(s)
	// Markdown links: [text](url) → <a href="url">text</a>.
	// Run before bold/italic so link text can contain formatting.
	for {
		start := strings.Index(s, "[")
		if start < 0 { break }
		mid := strings.Index(s[start:], "](")
		if mid < 0 { break }
		mid += start
		end := strings.Index(s[mid+2:], ")")
		if end < 0 { break }
		end += mid + 2
		text := s[start+1 : mid]
		url := s[mid+2 : end]
		s = s[:start] + `<a href="` + url + `">` + text + "</a>" + s[end+1:]
	}
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
	return s
}

// MarkdownToHTML converts markdown text to HTML.
func MarkdownToHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	in_code := false
	in_list := false
	in_ol := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Code blocks.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in_code {
				out.WriteString("</code></pre>\n")
				in_code = false
			} else {
				if in_list { out.WriteString("</ul>\n"); in_list = false }
				if in_ol { out.WriteString("</ol>\n"); in_ol = false }
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
