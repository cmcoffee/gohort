package core

import (
	"strings"
	"testing"
)

// A trigger matches a word or phrase, not any run of letters. As a plain
// substring, an agent with "PC" among its triggers (penal code) was offered
// for "the pcitrues just get stretched out", a typo of pictures on a turn
// about a web page's layout.
func TestATriggerMatchesWholeWords(t *testing.T) {
	for _, c := range []struct {
		trigger, msg string
		want         bool
	}{
		{"pc", "actually let's make the lay out more uniform the pcitrues just get stretched out", false},
		{"act", "actually, never mind", false},
		{"form", "make the layout more uniform", false},
		{"pc", "what does PC 372 cover?", true},
		{"pc", "is that a (PC) offense", true},
		{"penal code", "under the Penal Code, section 12", true},
		{"felony", "two felony counts", true},
		{"contract", "review these contracts", true},
		{"sentence", "she was sentenced in May", true},
		{"refund", "can I get a refund?", true},
		{"refund", "refunding it now", true},
		{"law", "my lawn needs mowing", false},
		{"law", "what does the law say", true},
		{"law", "the law's reach", true},
		{"c++", "a c++ question", true},
		{"café", "the café opens", true},
		{"café", "cafés nearby", true},
	} {
		if got := TriggersMatch([]string{c.trigger}, c.msg, nil); got != c.want {
			t.Errorf("%q in %q: got %v, want %v", c.trigger, c.msg, got, c.want)
		}
	}
	if !TriggersMatch([]string{"*.pdf"}, "see attached", []string{"Contract.PDF"}) {
		t.Error("a glob still matches attachment names")
	}
}

// The hint rides on the person's message, and a model that read it as theirs
// answered it ("the trigger about X is a false match"). It says they cannot
// see it.
func TestTheSkillHintSaysThePersonCannotSeeIt(t *testing.T) {
	if h := SkillTriggerHintBlock([]string{"Triage"}); !strings.Contains(h, "cannot see it, so never mention it") {
		t.Errorf("hint: %q", h)
	}
}
