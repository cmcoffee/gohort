package core

import "testing"

// TestRegisteredTunablesSane: every registered spec (the retrieval knobs plus
// every core knob that registered via init()) must have a coherent shape —
// non-empty key/category/label, a default inside [Min, Max], and Max >= Min.
// Catches a fan-out mistake like Min > Default (which would make the admin form
// reject the very default it shows).
func TestRegisteredTunablesSane(t *testing.T) {
	specs := AllTunableSpecs()
	if len(specs) == 0 {
		t.Fatal("no tunables registered")
	}
	seen := map[string]bool{}
	for _, s := range specs {
		if s.Key == "" || s.Category == "" || s.Label == "" {
			t.Errorf("%q: empty key/category/label (%q/%q/%q)", s.Key, s.Key, s.Category, s.Label)
		}
		if seen[s.Key] {
			t.Errorf("%q: duplicate key", s.Key)
		}
		seen[s.Key] = true
		if s.Max < s.Min {
			t.Errorf("%q: Max(%g) < Min(%g)", s.Key, s.Max, s.Min)
		}
		if s.Default < s.Min || s.Default > s.Max {
			t.Errorf("%q: Default(%g) outside [Min %g, Max %g]", s.Key, s.Default, s.Min, s.Max)
		}
	}
	t.Logf("validated %d registered tunables", len(specs))
}
