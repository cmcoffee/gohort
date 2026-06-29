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
