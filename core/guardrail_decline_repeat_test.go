package core

// The canned decline is the floor under every block. Uniform random from eight
// lines repeats about one time in eight, and a repeat is the single clearest
// tell that a machine answered — a user who hits the same wall twice and reads
// the identical sentence learns more from the repetition than any of the
// wording was allowed to tell them.

import "testing"

// TestDeclineNeverRepeatsBackToBack — consecutive picks always differ, over a
// long enough run that a uniform draw would certainly have repeated.
func TestDeclineNeverRepeatsBackToBack(t *testing.T) {
	prev := ""
	for i := 0; i < 200; i++ {
		got := guardrailSafeFallbackReply(nil)
		if got == prev {
			t.Fatalf("decline repeated back-to-back on draw %d: %q", i, got)
		}
		if !isGuardrailSafeFallback(got) {
			t.Fatalf("draw %d returned %q, which is not a built-in decline", i, got)
		}
		prev = got
	}
}

// TestDeclineStillUsesTheWholePool — suppressing the immediate repeat must not
// collapse the choice onto a subset.
func TestDeclineStillUsesTheWholePool(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		seen[guardrailSafeFallbackReply(nil)] = true
	}
	if len(seen) != len(guardrailSafeFallbacks) {
		t.Errorf("only %d of %d built-in declines were ever chosen", len(seen), len(guardrailSafeFallbacks))
	}
}

// TestSingleOwnerDeclineStillHonored — an owner who authored exactly one line
// meant it. The no-repeat rule must not invent variety they didn't ask for by
// reaching into the built-ins.
func TestSingleOwnerDeclineStillHonored(t *testing.T) {
	const only = "That's not something this desk handles."
	for i := 0; i < 20; i++ {
		if got := guardrailSafeFallbackReply([]string{only}); got != only {
			t.Fatalf("single owner decline was not used: %q", got)
		}
	}
}

// TestOwnerDeclinesAlsoAvoidRepeats — with a set to work from, the same rule
// applies to the owner's lines.
func TestOwnerDeclinesAlsoAvoidRepeats(t *testing.T) {
	custom := []string{"Not this one.", "That's a no from me.", "I'll pass on that."}
	prev := ""
	for i := 0; i < 100; i++ {
		got := guardrailSafeFallbackReply(custom)
		if got == prev {
			t.Fatalf("owner decline repeated back-to-back on draw %d: %q", i, got)
		}
		prev = got
	}
}
