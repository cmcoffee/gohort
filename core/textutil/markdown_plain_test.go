package textutil

import "testing"

// TestMarkdownToPlainUnderscores: snake_case identifiers must survive the
// plain-text flattening (the iMessage/SMS outbound path), while genuine
// _emphasis_ at word boundaries is still stripped.
func TestMarkdownToPlainUnderscores(t *testing.T) {
	cases := []struct{ in, want string }{
		// The regression: two snake_case tokens on one line had their
		// underscores paired and eaten.
		{"call link_entities and recall_about", "call link_entities and recall_about"},
		{"use store_fact then forget_fact", "use store_fact then forget_fact"},
		{"a single link_entities here", "a single link_entities here"},
		// Genuine emphasis at boundaries still flattens.
		{"this is _important_ now", "this is important now"},
		{"_lead_ then tail", "lead then tail"},
		{"end with _emphasis_", "end with emphasis"},
		// Bold/code unaffected by the change.
		{"**bold** and `code_value`", "bold and code_value"},
	}
	for _, c := range cases {
		if got := MarkdownToPlain(c.in); got != c.want {
			t.Errorf("MarkdownToPlain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A spoken reply reads the words, not the code or the addresses: a listener
// can use neither, and a speech model spells both out.
func TestSpeakableText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"code block dropped", "Run this:\n```\nrm -rf /tmp/x\n```\nThen restart.", "Run this:\nThen restart."},
		{"link keeps its words", "See [the release notes](https://example.com/notes) first.", "See the release notes first."},
		{"bare address goes", "It's at https://example.com/a?b=c, as before.", "It's at, as before."},
		{"address in brackets goes with them", "Done (details: https://example.com).", "Done (details:)."},
		{"bullets lose their mark", "Two things:\n- **milk**\n- bread", "Two things:\nmilk\nbread"},
		{"heading keeps its text", "## Weather\nSunny, 72.", "Weather\nSunny, 72."},
		{"plain text unchanged", "Sure, I'll call at noon.", "Sure, I'll call at noon."},
	}
	for _, c := range cases {
		if got := SpeakableText(c.in); got != c.want {
			t.Errorf("%s: SpeakableText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
