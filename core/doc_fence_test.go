package core

import (
	"strings"
	"testing"
)

// A markdown snippet routinely contains ``` blocks. The fixed
// three-backtick wrapper this file used everywhere would close at the
// snippet's first inner fence, handing the model a truncated script plus
// the remainder as loose prose.
func TestDocFence(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain code", "echo hi", "```"},
		{"empty", "", "```"},
		{"inline backtick", "var x = `y`", "```"},
		{"contains a fence", "text\n```go\nfmt.Println(1)\n```\nmore", "````"},
		{"contains a long fence", "a\n````\nnested\n````\nb", "`````"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DocFence(c.in); got != c.want {
				t.Errorf("DocFence(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The wrapper must never appear inside the content it wraps, or the
// block ends early wherever it does.
func TestDocFenceNeverAppearsInBody(t *testing.T) {
	bodies := []string{
		"# Doc\n\n```bash\nls -la\n```\n\nDone.",
		"````\nouter\n````",
		"no fences here at all",
		"`",
		"``",
	}
	for _, b := range bodies {
		f := DocFence(b)
		if strings.Contains(b, f) {
			t.Errorf("fence %q appears inside body %q — the block would close early", f, b)
		}
	}
}
