package textutil

// The inline-HTML pass-through keeps citation superscripts working and nothing
// else: no attributes but a safe href, no scripted link schemes.

import (
	"regexp"
	"strings"
	"testing"
)

func TestInlinePassthroughKeepsCitationsAndDropsScript(t *testing.T) {
	good := InlineMarkdownToHTML(`As shown<sup><a href="https://doi.example/10.1">18</a></sup>.`)
	if !strings.Contains(good, `<sup><a href="https://doi.example/10.1" target="_blank" rel="noopener noreferrer">18</a></sup>`) {
		t.Errorf("a citation should still render: %s", good)
	}
	for _, bad := range []string{
		`<a href="https://x.example" onmouseover="alert(1)">hover</a>`,
		`<a href="javascript:alert(1)">click</a>`,
		`<sup onclick="alert(1)">1</sup>`,
		`<a href=" JaVaScRiPt:alert(1)">x</a>`,
		`[click](javascript:alert(1))`,
		`<a href="//evil.example/x">x</a>`,
	} {
		out := InlineMarkdownToHTML(bad)
		low := strings.ToLower(out)
		if strings.Contains(low, "onmouseover") || strings.Contains(low, "onclick") ||
			strings.Contains(low, `href="javascript`) || strings.Contains(low, `href=" javascript`) ||
			strings.Contains(low, `href="//`) {
			t.Errorf("%s rendered unsafely: %s", bad, out)
		}
	}
}

// Every anchor the renderer makes is one anchor: a quoted href with no tag in
// it, and nothing after it but target and rel. Two shapes broke out of that:
// a URL inside a link's href that the bare-URL pass linked again, and a
// pass-through anchor parked right after a URL, which came back inside the
// href built around it. Both left an event handler loose in a tag.
func TestNoAnchorBreaksOutOfAnother(t *testing.T) {
	anchor := regexp.MustCompile(`<a\b[^>]*>`)
	shape := regexp.MustCompile(`^<a href="[^"<>]*"( target="_blank" rel="noopener noreferrer")?>$`)
	for _, in := range []string{
		`[x](https://a(https://x/onmouseover=location="javascript:alert%281%29"//))`,
		`[x](/a(https://x/onmouseover=location="javascript:alert%281%29"//))`,
		`see https://x<a href="https://a onmouseover=alert(document.domain) x">y</a>`,
		`https://x<sup><a href="https://a onmouseover=alert(1)">1</a></sup>`,
		"[z](https://z.example/\x00HTMLPROTECT0\x00)",
		`&lt;https://a onmouseover=alert(1)&gt; <https://b.example/x>`,
		`[x](//evil.example) [y](/\evil.example)`,
		// Emphasis must not reach into a link it did not make.
		`see https://x.example/a*b*c and https://y.example/a**b**c`,
	} {
		out := MarkdownToHTML(in)
		for _, tag := range anchor.FindAllString(out, -1) {
			if !shape.MatchString(tag) {
				t.Errorf("an anchor broke out:\n in  %q\n tag %s", in, tag)
			}
			if strings.HasPrefix(tag, `<a href="//`) || strings.HasPrefix(tag, `<a href="/\`) {
				t.Errorf("a link to another site passed as relative: %s", tag)
			}
		}
	}
	// What links are for still works: emphasis in link text, a linked image.
	out := MarkdownToHTML(`[**bold** link](https://b.example) and [![pic](data:image/png;base64,AAAA)](https://img.example)`)
	for _, want := range []string{`<strong>bold</strong> link</a>`, `<a href="https://img.example" target="_blank" rel="noopener noreferrer"><img src="data:image/png;base64,AAAA"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}
