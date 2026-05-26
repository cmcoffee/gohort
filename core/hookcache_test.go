package core

import "testing"

// Ensure hookCacheKey collapses near-identical LLM-generated queries
// into the same cache key. Previously these all missed because the
// cache keyed on exact query strings.
func TestHookCacheKey_collapsesVariants(t *testing.T) {
	variants := []string{
		`"moral crumple zone" AND "automation" AND ("liability" OR "responsibility")`,
		`moral crumple zone automation liability responsibility`,
		`responsibility liability automation moral crumple zone`,
		`the moral crumple zone and automation and liability or responsibility`,
	}
	want := hookCacheKey("CourtListener", variants[0])
	for i, v := range variants[1:] {
		got := hookCacheKey("CourtListener", v)
		if got != want {
			t.Errorf("variant %d key mismatch\n  want: %q\n  got:  %q\n  from: %q", i+1, want, got, v)
		}
	}
}

// Ensure materially different queries still get different keys.
func TestHookCacheKey_distinctDifferentQueries(t *testing.T) {
	a := hookCacheKey("CourtListener", "moral crumple zone automation")
	b := hookCacheKey("CourtListener", "strict product liability AI")
	if a == b {
		t.Errorf("expected distinct keys, got identical: %q", a)
	}
}

// Ensure the hook name differentiates otherwise identical queries.
func TestHookCacheKey_hookNameSeparation(t *testing.T) {
	a := hookCacheKey("CourtListener", "same query")
	b := hookCacheKey("PubMed", "same query")
	if a == b {
		t.Errorf("expected different hook names to produce different keys")
	}
}
