package textutil

import (
	"strings"
	"testing"
)

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
		{"code block dropped", "Run this:\n```\nrm -rf /tmp/x\n```\nThen restart.", "Run this.\nThen restart."},
		{"link keeps its words", "See [the release notes](https://example.com/notes) first.", "See the release notes first."},
		{"bare address goes", "It's at https://example.com/a?b=c, as before.", "It's at, as before."},
		{"address in brackets goes with them", "Done (details: https://example.com).", "Done (details:)."},
		{"bullets lose their mark", "Two things:\n- **milk**\n- bread", "Two things.\nmilk.\nbread"},
		{"heading keeps its text", "## Weather\nSunny, 72.", "Weather.\nSunny, 72."},
		{"abbreviations written out", "**Open** Sat, Nov 2.", "Open Saturday, November 2nd."},
		{"plain text unchanged", "Sure, I'll call at noon.", "Sure, I'll call at noon."},
		// The break between lines is lost on the way to the voice, so each
		// line ends as a sentence.
		{"a colon line ends", "What you'd need now:\nDemocrats control 52 of 100 seats.", "What you'd need now.\nDemocrats control 52 of 100 seats."},
		{"heading and list items end", "## Senate\n- Democrats: 47\n- Republicans: 53", "Senate.\nDemocrats: 47.\nRepublicans: 53"},
		{"a line that runs on is left", "We looked at the numbers and\nthey hold up", "We looked at the numbers and\nthey hold up"},
		{"a comma runs on", "First,\nSecond.", "First,\nSecond."},
		{"a quoted stop ends the line", "He said \"go.\"\nThen left", "He said \"go.\"\nThen left"},
		{"a line the page sends ends", "What you'd need now:\n", "What you'd need now."},
		{"emoji go", "Great news 🎉 we won 🇺🇸!", "Great news we won!"},
		{"smileys go", "Done :) see you ;-) <3", "Done see you"},
		{"a time is not a smiley", "Meet at 3:30 (or so).", "Meet at 3:30 (or so)."},
	}
	for _, c := range cases {
		if got := SpeakableText(c.in); got != c.want {
			t.Errorf("%s: SpeakableText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// SpeakablePlain is what a person sees beside speech: the same text the voice
// gets, but as written. Only SpeakableText writes "v0.9" out as words.
func TestSpeakablePlainStaysWritten(t *testing.T) {
	in := "**Sat** at 10.0.0.1 on v0.9, see https://example.com"
	if got, want := SpeakablePlain(in), "Sat at 10.0.0.1 on v0.9, see"; got != want {
		t.Errorf("SpeakablePlain = %q, want %q", got, want)
	}
	if got := SpeakableText(in); !strings.Contains(got, "10 dot 0 dot 0 dot 1") || !strings.Contains(got, "version 0") {
		t.Errorf("SpeakableText = %q, want the address and version said as words", got)
	}
}
