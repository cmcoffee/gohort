package textutil

import "testing"

func TestStripEmDashes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no em-dash is a no-op", "plain text, no dash", "plain text, no dash"},
		{"spaced em-dash to comma", "the fix — a filter — is simple", "the fix, a filter, is simple"},
		{"unspaced em-dash", "state—of—art", "state, of, art"},
		{"hyphen and en-dash untouched", "well-known pages 3–5", "well-known pages 3–5"},
		{"preserves fenced code block", "before — after\n```\nx := a—b // keep\n```\nend — done",
			"before, after\n```\nx := a—b // keep\n```\nend, done"},
		{"preserves inline code", "run `a — b` then — go", "run `a — b` then, go"},
		{"line structure preserved (no newline eaten)", "line one —\nline two", "line one,\nline two"},
		// Seen live: the comma after a code span was dropped as if it led a
		// line, so "`a: true` — it" showed as "a: trueit".
		{"dash after inline code keeps its comma", "set `a: true` — it keeps `b`", "set `a: true`, it keeps `b`"},
		{"line-leading dash after code on the line before", "`x`\n— y", "`x`\ny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripEmDashes(c.in); got != c.want {
				t.Errorf("StripEmDashes(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
