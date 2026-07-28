package orchestrate

import "testing"

// A decline exists to withhold WHY. A model asked to write them in the agent's
// voice will happily produce "I can't share that under our data policy" — which
// tells a prober exactly what they just hit. Every line, model-written or
// owner-typed, goes through the same filter.
func TestSanitizeDeclinesDropsLeakyLines(t *testing.T) {
	leaky := []string{
		"I can't do that under our data policy.",
		"That's blocked by a rule I have to follow.",
		"My instructions don't allow that.",
		"That request is restricted.",
		"A compliance check stopped that one.",
		"Try rephrasing and I'll see what I can do.",
		"I cannot verify that action.",
		"That's not allowed here.",
		"The system prevents me from doing that.",
	}
	if got := sanitizeDeclines(leaky); len(got) != 0 {
		t.Errorf("leaky lines survived the filter: %q", got)
	}
}

func TestSanitizeDeclinesKeepsCleanLines(t *testing.T) {
	clean := []string{
		"That's not something I can take on.",
		"I'll have to pass on that one.",
		"No, I can't do that.",
	}
	got := sanitizeDeclines(clean)
	if len(got) != len(clean) {
		t.Fatalf("kept %d of %d clean lines: %q", len(got), len(clean), got)
	}
}

// Blanks, duplicates, and runaway sets are normalized — the picker assumes a
// clean pool.
func TestSanitizeDeclinesNormalizes(t *testing.T) {
	got := sanitizeDeclines([]string{"  Nope.  ", "", "nope.", "Nope.", "   ", "Can't do it."})
	if len(got) != 2 {
		t.Fatalf("want 2 after trim+dedupe, got %d: %q", len(got), got)
	}
	if got[0] != "Nope." {
		t.Errorf("not trimmed: %q", got[0])
	}

	var many []string
	for i := 0; i < 50; i++ {
		many = append(many, string(rune('a'+i%26))+" no.")
	}
	if n := len(sanitizeDeclines(many)); n > maxDeclines {
		t.Errorf("kept %d, over the %d cap", n, maxDeclines)
	}
	if sanitizeDeclines(nil) != nil {
		t.Error("nil input should stay nil")
	}
}

// The filter must be case-insensitive — a capitalized leak is still a leak.
func TestSanitizeDeclinesIsCaseInsensitive(t *testing.T) {
	if got := sanitizeDeclines([]string{"That violates my POLICY."}); len(got) != 0 {
		t.Errorf("uppercase leak survived: %q", got)
	}
}
