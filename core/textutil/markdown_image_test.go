package textutil

// Markdown images render as <img> only for this server's own paths and
// embedded rasters. The renderer also draws model output, and an image is
// fetched as soon as the page shows it, so a remote one in an injected reply
// would carry data off with no click; those stay links.

import (
	"strings"
	"testing"
)

func TestMarkdownImagesRenderOnlyWhenSafe(t *testing.T) {
	cases := []struct {
		md      string
		wantImg bool
	}{
		{"![Login screen](/scribe/img?g=abc&i=def)", true},
		{"![x](data:image/png;base64,iVBORw0KGgo=)", true},
		{"![x](https://elsewhere.example/?q=secret)", false},
		{"![x](//elsewhere.example/a.png)", false},
		{"![x](data:image/svg+xml;base64,PHN2Zz4=)", false},
		{"![x](javascript:alert(1))", false},
	}
	for _, c := range cases {
		html := MarkdownToHTML(c.md)
		if got := strings.Contains(html, "<img"); got != c.wantImg {
			t.Errorf("%s: <img> = %v, want %v\n%s", c.md, got, c.wantImg, html)
		}
	}
	// The query's & survives as an attribute-safe &amp;, and an asterisk in the
	// alt text is not turned into emphasis inside the tag.
	html := MarkdownToHTML("See ![the *new* screen](/scribe/img?g=a&i=b) here.")
	if !strings.Contains(html, `src="/scribe/img?g=a&amp;i=b"`) || !strings.Contains(html, `alt="the *new* screen"`) {
		t.Errorf("image tag mangled:\n%s", html)
	}
	if strings.Contains(html, "!<a") {
		t.Errorf("an image read as a link:\n%s", html)
	}
}

// A page converted back to markdown keeps its pictures.
func TestHTMLToMarkdownKeepsImages(t *testing.T) {
	md := HTMLToMarkdown(`<p>Before</p><img loading="lazy" alt="Login" src="/scribe/img?g=a&amp;i=b"><p>After</p>`)
	if !strings.Contains(md, "![Login](/scribe/img?g=a&i=b)") {
		t.Errorf("image lost or mangled: %q", md)
	}
	if md := HTMLToMarkdown(`<img alt="no source">`); strings.Contains(md, "![") {
		t.Errorf("an image with no src should drop: %q", md)
	}
}
