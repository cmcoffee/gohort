package textutil

import "testing"

func TestStripFillerClassic(t *testing.T) {
	tests := []struct{ name, in, want string }{
		// The reply that prompted this, verbatim.
		{"the observed tic",
			"Ha! That's a classic tech dad joke",
			"Ha! That's a tech dad joke"},

		{"determiners", "the classic mistake", "the mistake"},
		{"this", "this classic pattern", "this pattern"},
		{"possessive", "your classic setup problem", "your setup problem"},
		{"capitalised determiner", "A classic blunder", "A blunder"},
		{"mid sentence", "It is the classic tradeoff here.", "It is the tradeoff here."},
		{"twice", "a classic case of a classic misread", "a case of a misread"},

		// The article has to follow the noun it now sits in front of.
		{"article agreement", "that's a classic error", "that's an error"},
		{"article agreement capitalised", "A classic oversight", "An oversight"},
		{"consonant keeps a", "a classic blunder", "a blunder"},

		// Head-noun use has no following modifier, so it never matches.
		{"head noun", "That's a classic.", "That's a classic."},
		{"head noun with of", "a classic of the genre", "a classic of the genre"},
		{"head noun with that", "a classic that everyone knows", "a classic that everyone knows"},
		{"head noun with and", "a classic and a favourite", "a classic and a favourite"},
		{"head noun with is", "the classic is still the best", "the classic is still the best"},
		{"predicate", "the film is classic", "the film is classic"},

		// Literal senses survive.
		{"classic car", "he restored a classic car", "he restored a classic car"},
		{"classic edition", "the classic edition shipped", "the classic edition shipped"},
		{"classic rock", "some classic rock came on", "some classic rock came on"},

		// Not the same word.
		{"classical", "a classical education", "a classical education"},
		{"proper noun", "a Classic Mode toggle", "a Classic Mode toggle"},

		{"absent", "nothing to do here", "nothing to do here"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripFillerClassic(tc.in); got != tc.want {
				t.Errorf("StripFillerClassic(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Code is quoted text, not prose. Rewriting inside it would corrupt something
// the reader is meant to run, which is the same reason StripEmDashes skips it.
func TestStripFillerClassicLeavesCodeAlone(t *testing.T) {
	tests := []struct{ in, want string }{
		{"use `a classic mode` here", "use `a classic mode` here"},
		{"```\nvar a classic thing\n```", "```\nvar a classic thing\n```"},
		// Prose on either side of a code span is still normalised.
		{"a classic bug in `a classic call`, a classic slip",
			"a bug in `a classic call`, a slip"},
	}
	for _, tc := range tests {
		if got := StripFillerClassic(tc.in); got != tc.want {
			t.Errorf("StripFillerClassic(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
		}
	}
}

// The two house-style rules compose: the reply that started this broke both.
func TestHouseStyleRulesCompose(t *testing.T) {
	in := "That's a classic tech dad joke — hope it helps!"
	want := "That's a tech dad joke, hope it helps!"
	if got := StripFillerClassic(StripEmDashes(in)); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
