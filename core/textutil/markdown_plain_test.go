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
