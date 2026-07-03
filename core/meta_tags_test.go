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

func TestStripToolCallTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{`before <tool_call>{"name":"x"}</tool_call> after`, "before  after"},
		{"a\n<tool_call>\ncall\n</tool_call>\nb", "a\n\nb"},
		{"text <function=foo>{}</function> end", "text  end"},
		{"run <tool_code>print(1)</tool_code> now", "run  now"},
		{"keep <tool_call> only", "keep  only"},   // orphan opener
		{"answer </tool_call>", "answer"},         // stray closer
		{"no tool markup here", "no tool markup here"},
		{"", ""},
		// \b boundary must NOT eat legit words/prose that merely start with the
		// tag name, or ordinary angle brackets.
		{"see <functionality> and a < b > c", "see <functionality> and a < b > c"},
	}
	for _, c := range cases {
		if got := StripToolCallTags(c.in); got != c.want {
			t.Errorf("StripToolCallTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
