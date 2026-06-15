package core

import "testing"

func TestStripMetaTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Here's a meme for you! [ATTACH: funny-meme.png]", "Here's a meme for you!"},
		{"Done [ATTACH: a.png, cleanup=true] enjoy", "Done  enjoy"},
		{"text <gohort-meta>note to self</gohort-meta> more", "text  more"},
		{"a\n<gohort-meta>\ninternal\nplan\n</gohort-meta>\nb", "a\n\nb"},
		{"shell out <<<ATTACH:image/png>>>base64...<<<END>>> ok", "shell out  ok"},
		{"no markers here", "no markers here"},
		{"", ""},
		{"keep [brackets] and [normal: text]", "keep [brackets] and [normal: text]"},
	}
	for _, c := range cases {
		if got := StripMetaTags(c.in); got != c.want {
			t.Errorf("StripMetaTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
