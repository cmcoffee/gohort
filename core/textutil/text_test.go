package textutil

import (
	"strings"
	"testing"
)

// NormalizeHeadingLinks rewrites internal anchors from their link text so
// LLM slug drift can't break TOC / cross-reference links.
func TestNormalizeHeadingLinks(t *testing.T) {
	md := `Intro paragraph.

## Table of Contents
- [1. Setup](#1-setup)
- [2. Distribution & Version](#2-distribution--version)
- [3. API Keys (sensitive)](#wrong-guess)

## 1. Setup

See [3. API Keys (sensitive)](#3-api-keys-sensitive) for the secrets.
Also see [an external doc](https://example.com/#frag) and [no such heading](#missing).

## 2. Distribution & Version

## 3. API Keys (sensitive)
`
	out := NormalizeHeadingLinks(md)

	// Every link whose text matches a heading gets the renderer's slug —
	// including ones where the model guessed the anchor wrong.
	for _, want := range []string{
		"[1. Setup](#1-setup)",
		// The renderer's slug keeps the double dash from " & " — the
		// normalizer must land on the renderer's exact spelling.
		"[2. Distribution & Version](#2-distribution--version)",
		"[3. API Keys (sensitive)](#3-api-keys-sensitive)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("normalized output missing %q", want)
		}
	}
	if strings.Contains(out, "#wrong-guess") {
		t.Errorf("stale anchor survived normalization:\n%s", out)
	}
	// Non-matching and external links pass through untouched.
	if !strings.Contains(out, "[no such heading](#missing)") {
		t.Error("link with no matching heading should be untouched")
	}
	if !strings.Contains(out, "(https://example.com/#frag)") {
		t.Error("external link should be untouched")
	}
}

// Headings inside fenced code blocks don't mint slugs, so links can't be
// retargeted at phantom headings.
func TestNormalizeHeadingLinksSkipsCodeFences(t *testing.T) {
	md := "## Real\n\n```\n## Fake Heading\n```\n\n[Fake Heading](#kept-as-is)\n"
	out := NormalizeHeadingLinks(md)
	if !strings.Contains(out, "[Fake Heading](#kept-as-is)") {
		t.Errorf("code-fenced heading must not capture links:\n%s", out)
	}
}

func TestHeadingLevel(t *testing.T) {
	cases := map[string]int{
		"# h1":          1,
		"## h2":         2,
		"### h3":        3,
		"#### h4":       4,
		"##### h5":      5,
		"###### h6":     6,
		"####### nope":  0, // 7 hashes is not a heading
		"#no-space":     0,
		"text":          0,
		"#### 1. Setup": 4,
	}
	for in, want := range cases {
		if got := headingLevel(in); got != want {
			t.Errorf("headingLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMarkdownToHTML_DeepHeadings(t *testing.T) {
	md := "## Section\n\n#### 1. Node Shows NotReady\n\nSome text.\n\n##### Deeper\n\nMore."
	html := MarkdownToHTML(md)
	for _, want := range []string{"<h2", "<h4", ">1. Node Shows NotReady<", "<h5", ">Deeper<"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in:\n%s", want, html)
		}
	}
	// The literal #### must NOT leak as text.
	if strings.Contains(html, "#### 1. Node") {
		t.Errorf("raw #### heading leaked:\n%s", html)
	}
}

// Headings inside fenced code must NOT become HTML headings or shift slugs.
func TestMarkdownToHTML_HeadingsInCode(t *testing.T) {
	md := "# Title\n\n```bash\n#### not a heading\nkubectl get nodes\n```\n\n## After"
	html := MarkdownToHTML(md)
	if !strings.Contains(html, "<pre><code>") {
		t.Errorf("code fence not rendered:\n%s", html)
	}
	if strings.Contains(html, "<h4") {
		t.Errorf("heading inside code block was parsed as a real heading:\n%s", html)
	}
}

func TestMarkdownLinks_SourcesLine(t *testing.T) {
	md := "Sources: [OneUptime: Containerize Go](https://oneuptime.com/blog/post/x/view) | [Snyk: Go](https://snyk.io/blog/containerizing-go/)"
	html := MarkdownToHTML(md)
	// Both links convert to anchors with the title as the visible text.
	if !strings.Contains(html, `<a href="https://oneuptime.com/blog/post/x/view" target="_blank" rel="noopener noreferrer">OneUptime: Containerize Go</a>`) {
		t.Errorf("first source link not converted:\n%s", html)
	}
	if !strings.Contains(html, `>Snyk: Go</a>`) {
		t.Errorf("second source link not converted:\n%s", html)
	}
	// No literal markdown-link brackets left over.
	if strings.Contains(html, "](http") {
		t.Errorf("literal markdown-link syntax leaked:\n%s", html)
	}
}

func TestMarkdownLinks_BareURLStillAutolinks(t *testing.T) {
	html := MarkdownToHTML("See https://example.com for details.")
	if !strings.Contains(html, `<a href="https://example.com"`) {
		t.Errorf("bare URL not autolinked:\n%s", html)
	}
}
