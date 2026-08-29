package prompts

import (
	"strings"
	"testing"
)

// The rule has to be VISIBLE, not merely present. Thirteen framework clauses
// accumulated in the assembler with the Prompts page showing none of them, so a
// new one that repeated that would be the wrong fix to the wrong problem.
func TestToolVisibilityRuleIsOnThePromptsPage(t *testing.T) {
	var found *PromptBlock
	for _, b := range AllPromptBlocks() {
		if b.Key == ToolVisibilityKey {
			cp := b
			found = &cp
			break
		}
	}
	if found == nil {
		t.Fatal("the rule is injected but invisible; register it")
	}
	if !found.Builtin || found.Text == "" || found.Gate == "" {
		t.Errorf("block is incomplete: %+v", *found)
	}
}

// What the clause must say. Pinned because the failure is subtle in both
// directions: too weak and an agent keeps reacting to results the reader never
// saw, too strong and it pastes a calendar API's JSON back at someone who asked
// it to book a meeting.
func TestToolVisibilityRuleCoversBothCases(t *testing.T) {
	text := ToolVisibilityClause()
	for _, want := range []string{
		"does NOT see",   // the fact that was never stated anywhere
		"your own words", // what the reader actually receives
		"ACTION",         // the carve-out, or the rule over-applies
	} {
		if !strings.Contains(text, want) {
			t.Errorf("clause no longer says %q:\n%s", want, text)
		}
	}
	// A rule about repeating tool output must not model the tic the style rules
	// exist to remove.
	if strings.Contains(text, " — ") {
		t.Error("the clause uses an em-dash as punctuation")
	}
}

// It ships ON, and switching it off removes it from the prompt entirely rather
// than leaving a header with nothing under it.
func TestToolVisibilityRuleShipsOnAndCanBeSwitchedOff(t *testing.T) {
	withStore(t)
	if ToolVisibilityClause() == "" {
		t.Fatal("the rule should ship enabled")
	}
	SetPromptBlockEnabled(ToolVisibilityKey, false)
	if got := ToolVisibilityClause(); got != "" {
		t.Errorf("disabled rule still injected: %q", got)
	}
	SetPromptBlockEnabled(ToolVisibilityKey, true)
	if ToolVisibilityClause() == "" {
		t.Error("re-enabling did not restore it")
	}
}
