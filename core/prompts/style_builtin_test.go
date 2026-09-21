package prompts

// The shipped prompt-only rule.
//
// Its subject is a shape of reasoning rather than a character or a word, so
// nothing can strip it at the boundary: it only ASKS. That is a supported kind
// here, and the page shows the difference, but it means the wording has to
// carry the whole job.

import (
	"strings"
	"testing"
)

func builtinRule(t *testing.T, key string) StyleRule {
	t.Helper()
	for _, r := range BuiltinStyleRules() {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no builtin rule %q", key)
	return StyleRule{}
}

// On by default, like the other shipped rules, and reaching the prompt.
func TestTheNarrationRuleShipsEnabled(t *testing.T) {
	r := builtinRule(t, RuleNoAttemptNarration)
	if !r.Builtin {
		t.Error("the rule is not marked as shipped")
	}
	if !PromptBlockEnabled(r.Key) {
		t.Error("the rule is off by default")
	}
	if !strings.Contains(StyleClause(), "Do not narrate attempts") {
		t.Error("the rule does not reach the style clause")
	}
}

// The exception is the whole reason the rule is safe. Without it the model is
// being taught to hide a failure that CHANGED the answer along with one that
// changed nothing, which is the opposite of the intent.
func TestTheRuleKeepsTheMaterialFailureAudible(t *testing.T) {
	text := builtinRule(t, RuleNoAttemptNarration).Text
	for _, want := range []string{"not optional", "changed the ANSWER", "could not reach a source"} {
		if !strings.Contains(text, want) {
			t.Errorf("the exception is missing %q, so the rule reads as blanket silence", want)
		}
	}
}

// It must not model what its sibling rule forbids, and must not narrate an
// attempt while telling the model not to.
func TestTheRuleObeysTheOtherRules(t *testing.T) {
	text := builtinRule(t, RuleNoAttemptNarration).Text
	if strings.ContainsRune(text, '—') {
		t.Error("the rule uses an em-dash, which the sibling rule forbids")
	}
	if strings.Contains(strings.ToLower(text), "classic") {
		t.Error("the rule uses the filler word another rule forbids")
	}
}

// Turning it off takes it out of the prompt, like every other rule.
func TestTheNarrationRuleCanBeTurnedOff(t *testing.T) {
	// A store, because the toggle is a no-op without one and the test would
	// pass by doing nothing.
	withStore(t)
	if !strings.Contains(StyleClause(), "Do not narrate attempts") {
		t.Fatal("the rule is not in the clause to begin with")
	}
	SetPromptBlockEnabled(RuleNoAttemptNarration, false)
	if strings.Contains(StyleClause(), "Do not narrate attempts") {
		t.Error("the rule survives being switched off")
	}
	SetPromptBlockEnabled(RuleNoAttemptNarration, true)
	if !strings.Contains(StyleClause(), "Do not narrate attempts") {
		t.Error("the rule does not come back when switched on")
	}
}
