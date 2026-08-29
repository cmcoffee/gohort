package textutil

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/prompts"
)

// The two shipped style rules must be registered WITH their enforcers, and this
// has to be asserted HERE: the registration happens in this package's init(), so
// a test living in core/prompts would never load it and would pass while the
// binding was missing.
//
// What it guards is the split that made these worth moving: a sentence in the
// system prompt asking for something, and a transform enforcing it, with nothing
// tying them together. Sharing a key, turning the rule off stops both.
func TestHouseStyleRulesCarryEnforcers(t *testing.T) {
	enforced := map[string]bool{}
	for _, k := range prompts.RuleEnforcerKeys() {
		enforced[k] = true
	}
	rules := map[string]prompts.StyleRule{}
	for _, r := range prompts.AllStyleRules() {
		rules[r.Key] = r
	}

	for _, key := range []string{RuleNoEmDash, RuleNoFillerClassic} {
		if !enforced[key] {
			t.Errorf("no enforcer bound to %q; the rule would only ask", key)
		}
		r, ok := rules[key]
		if !ok {
			t.Errorf("%q is enforced but invisible as a style rule", key)
			continue
		}
		if !r.Builtin {
			t.Errorf("%q should be builtin", key)
		}
		if r.Text == "" {
			t.Errorf("%q has no text to show an operator", key)
		}
	}
}

// The clause is what actually reaches the model, so pin its shape: both rules,
// numbered, in one bracket.
func TestStyleClauseCarriesBothShippedRules(t *testing.T) {
	got := prompts.StyleClause()
	if !strings.HasPrefix(got, "[Style:") || !strings.HasSuffix(got, "]") {
		t.Fatalf("clause shape changed: %q", got)
	}
	for _, want := range []string{"(1)", "(2)", "classic", "em-dash"} {
		if !strings.Contains(got, want) {
			t.Errorf("clause is missing %q: %s", want, got)
		}
	}
	// A rule against em-dashes must not USE one as punctuation, or it models the
	// behaviour it forbids. Naming the character in quotes is not using it, and
	// is unavoidable: the rule has to say which character it means.
	if strings.Contains(got, " — ") {
		t.Error("the style clause uses an em-dash as punctuation")
	}
	if !strings.Contains(got, "U+2014") {
		t.Error("the em-dash rule should name the codepoint, not only the glyph")
	}
}

// Disabling a style rule must stop its transform, which is the whole reason the
// two halves share a key.
func TestStyleRuleTransformsRunThroughTheRegistry(t *testing.T) {
	if got := prompts.ApplyRuleEnforcers("a — b"); got != "a, b" {
		t.Fatalf("em-dash rule did not run through the registry: %q", got)
	}
	if got := prompts.ApplyRuleEnforcers("that's a classic mistake"); got != "that's a mistake" {
		t.Errorf("classic rule did not run through the registry: %q", got)
	}
}
