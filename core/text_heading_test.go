package core

import (
	"strings"
	"testing"
)

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
