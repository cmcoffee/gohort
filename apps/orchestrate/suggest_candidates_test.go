package orchestrate

import (
	"strings"
	"testing"
)

// TestSuggestionCandidatesSurviveTheShapesModelsActuallyReturn. "Five names,
// one per line, nothing else" is an instruction, not a guarantee: models
// number them, bullet them, quote them, and add a closing sentence. A name is
// chosen rather than computed, so the parse has to be forgiving enough that
// the user still gets a list to pick from.
func TestSuggestionCandidatesSurviveTheShapesModelsActuallyReturn(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"plain lines", "Ada\nScout\nDeploy Watch", []string{"Ada", "Scout", "Deploy Watch"}},
		{"numbered", "1. Ada\n2) Scout\n3 - Deploy Watch", []string{"Ada", "Scout", "Deploy Watch"}},
		{"bulleted and quoted", "- \"Ada\"\n* 'Scout'\n• Deploy Watch", []string{"Ada", "Scout", "Deploy Watch"}},
		{"blank lines between", "Ada\n\nScout\n\n", []string{"Ada", "Scout"}},
		{"duplicates collapse", "Ada\nada\nScout", []string{"Ada", "Scout"}},
		{"one line stays one", "Ada", []string{"Ada"}},
	}
	for _, c := range cases {
		got := suggestionCandidates(c.raw)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// A model that ignores "nothing else" and writes prose must not turn a
	// sentence into a name.
	prose := suggestionCandidates("Here are five names you might like:\nAda\nScout")
	for _, p := range prose {
		if strings.Contains(p, "names you might like") {
			t.Errorf("a sentence was offered as a name: %q", p)
		}
	}
	if len(prose) != 2 {
		t.Errorf("the real names were lost alongside the preamble: %v", prose)
	}

	// And the cap keeps a runaway list from becoming a wall of buttons.
	many := suggestionCandidates(strings.Repeat("Ada1\nAda2\nAda3\nAda4\n", 4))
	if len(many) > 6 {
		t.Errorf("returned %d candidates; the list is meant to be pickable", len(many))
	}
}
