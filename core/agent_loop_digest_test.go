package core

import (
	"strings"
	"testing"
)

// The section ranking names a loop clause as its own section, instead of
// counting it under the last heading before it: the clauses come after every
// heading, so 8k of standing rules read as "Tools available" or "Other phases
// in this workflow".
func TestTheSectionRankingNamesTheLoopClauses(t *testing.T) {
	prompt := "Persona.\n\n## Tools available\nweb_search, fetch_url\n\n[Grounding: state a precise specific only when " + strings.Repeat("x", 400) + "]\n\n[Secrets: never ask for a key]"
	got := systemSectionRanking(prompt)
	for _, want := range []string{`"[Grounding]"=`, `"[Secrets]"=`, `"Tools available"=`} {
		if !strings.Contains(got, want) {
			t.Errorf("ranking lacks %s: %s", want, got)
		}
	}
	if strings.Index(got, `"[Grounding]"`) > strings.Index(got, `"Tools available"`) {
		t.Errorf("the long clause should outrank the short section: %s", got)
	}
}
