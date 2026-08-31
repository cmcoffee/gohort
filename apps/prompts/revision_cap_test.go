package prompts

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Same guard as the guides revision cap: an unregistered key resolves to 0
// through TuneInt, and a 0 here prunes every block's undo history on the next
// edit — including the pre-Optimize snapshot, which is the reason the history
// exists.
func TestPromptRevisionCapResolvesToItsDefault(t *testing.T) {
	if got := maxRevisionsPerBlock(); got != 10 {
		t.Fatalf("maxRevisionsPerBlock() = %d, want the registered default 10 — a 0 here prunes every block's history on its next edit", got)
	}
	var spec *TunableSpec
	for _, s := range AllTunableSpecs() {
		if s.Key == TunablePromptRevisionCap {
			c := s
			spec = &c
		}
	}
	if spec == nil {
		t.Fatal("the prompt revision cap is not registered, so no admin can reach it")
	}
	if spec.Min < 1 {
		t.Errorf("Min %g would let an operator turn the history off by accident", spec.Min)
	}
	if spec.App != (&PromptsApp{}).WebPath() {
		t.Errorf("App claim %q does not match the app's WebPath %q", spec.App, (&PromptsApp{}).WebPath())
	}
}
