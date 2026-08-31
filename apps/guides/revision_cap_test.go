package guides

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The failure this guards is silent and destructive. TuneInt returns 0 for a key
// nobody registered, and a cap of 0 means saveGuideRev trims every guide to no
// history at all on the next save — the undo trail deleted by a typo, with
// nothing logged. So check the accessor resolves to the shipped default rather
// than trusting that the key strings match.
func TestRevisionCapResolvesToItsDefault(t *testing.T) {
	if got := maxRevisions(); got != 50 {
		t.Fatalf("maxRevisions() = %d, want the registered default 50 — a 0 here silently wipes every guide's history on its next save", got)
	}
	var spec *TunableSpec
	for _, s := range AllTunableSpecs() {
		if s.Key == TunableGuideRevisionCap {
			c := s
			spec = &c
		}
	}
	if spec == nil {
		t.Fatal("the revision cap is not registered, so no admin can reach it")
	}
	if spec.Min < 1 {
		t.Errorf("Min %g would let an operator set a cap of zero — history off by accident", spec.Min)
	}
	// Every knob claiming an app must name a path an app actually serves, or it
	// shows up under nothing. core warns at boot; this fails at build time.
	if spec.App != (&Guides{}).WebPath() {
		t.Errorf("App claim %q does not match the app's WebPath %q", spec.App, (&Guides{}).WebPath())
	}
}
