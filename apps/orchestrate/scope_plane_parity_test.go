package orchestrate

import "testing"

// The three scope planes are documented as offering the same set of targets.
// The tool plane excluded clone-only template seeds outright; the source and
// pipeline planes did not — and seed-kb is a live seed record that listAgents
// returns, so it was offered as a scope target on two planes of three. A grant
// could be made, or kept visible, on a template that is never runnable.
//
// Asserted on the predicate the planes share rather than by driving three
// handlers, because what diverged is which predicates each plane consulted.
func TestCloneOnlySeedIsNotAScopeTarget(t *testing.T) {
	if !isCloneOnlySeed("seed-kb") {
		t.Fatal("seed-kb is no longer clone-only; the three planes' exclusions need revisiting together")
	}
	if isCloneOnlySeed("some-user-agent") {
		t.Error("an ordinary agent must not be treated as a clone-only seed")
	}
}
