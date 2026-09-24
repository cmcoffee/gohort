package textutil

// The inline-HTML pass-through keeps citation superscripts working and nothing
// else: no attributes but a safe href, no scripted link schemes.

import (
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
