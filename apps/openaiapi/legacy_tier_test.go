package openaiapi

import "testing"

// Model ids from before the rename are still tier names, and the lead one is
// still the lead.
func TestTheOldModelIdsAreStillTiers(t *testing.T) {
	for _, id := range []string{"gohort", "gohort-worker", "gohort-lead"} {
		if !isTierName(id) {
			t.Errorf("%s is no longer a tier", id)
		}
	}
	if canonicalTier("gohort-lead") != "lead" || canonicalTier("gohort-worker") != "worker" {
		t.Error("the old ids map to the wrong tier")
	}
}
