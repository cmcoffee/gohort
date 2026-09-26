package orchestrate

import (
	"strings"
	"testing"
)

// The intake renderer's steps. Pinned at the source because they run in the
// browser: a stepped form's buttons pick rather than send, a step the answers
// have hidden is neither required nor sent, and a live select refetches when an
// answer it names changes.
func TestTheIntakeFormRunsAsSteps(t *testing.T) {
	src := orchestrateWebAssets
	for _, want := range []string{
		// Steps are recognised from the fields themselves.
		"var stepped = fields.some(function(f) { return f.show_when || f.options_from; });",
		// A stepped form's button selects and re-evaluates instead of submitting.
		"if (stepped) {\n                  inp.querySelectorAll('.ui-orch-intake-button')",
		// The same grammar as the settings forms, not a local copy.
		"window.uiMatchesWhen(f.show_when, m)",
		// Hidden steps drop out of both the required check and what is sent.
		"fields = fields.filter(function(f) { return !isShown || isShown(f); });",
		"collectIntake(fields, built.inputs, built.isShown)",
		// Live options follow the answers they name, and a stale reply loses.
		"if (lastURL[f.name] !== url) return;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the intake renderer lost a step behaviour: %q", want)
		}
	}
}
