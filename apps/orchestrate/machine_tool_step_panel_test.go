package orchestrate

// A tool step has no model, so nothing in the tool-limit panel can apply to
// it: runToolPhase builds its own pool of exactly the named tool. The editor
// offered three choices about what a model may reach anyway, one labelled
// "this step only decides", under a control reading "No model, no tokens".

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestToolStepHidesToolLimitsWhenNothingIsStored(t *testing.T) {
	if phaseShowsTools(MachinePhase{Name: "search", Tool: "search_acme_knowledge"}) {
		t.Error("a tool step with nothing stored must not offer controls that do nothing")
	}
}

// Kept when something IS stored: it takes effect again the moment the tool is
// cleared, and a restriction nobody can see is worse than one they can.
func TestToolStepKeepsStoredLimitsVisible(t *testing.T) {
	for _, p := range []MachinePhase{
		{Name: "a", Tool: "x", Tools: []string{"search_docs"}},
		{Name: "b", Tool: "x", Reach: ReachRead},
	} {
		if !phaseShowsTools(p) {
			t.Errorf("a stored limit must stay visible on a tool step: %+v", p)
		}
	}
}

func TestKeptLimitsSayTheyAreNotInEffect(t *testing.T) {
	fields := phaseToolFields(MachinePhase{
		Name: "a", Tool: "search_acme_knowledge", Reach: ReachNone,
	}, editorCatalog{})
	var head, help string
	for _, f := range fields {
		if f.Type == "header" {
			head, help = f.Label, f.Help
			break
		}
	}
	if !strings.Contains(head, "Kept, not applied") {
		t.Errorf("first header should say the panel is inert, got %q", head)
	}
	if !strings.Contains(help, "search_acme_knowledge") {
		t.Errorf("should name the tool the step calls: %q", help)
	}
	if !strings.Contains(help, "clear the tool above") {
		t.Errorf("should say what to change: %q", help)
	}
}

func TestModelStepStillShowsItsToolLimits(t *testing.T) {
	if !phaseShowsTools(MachinePhase{Name: "think"}) {
		t.Error("a model step must still get the tool-limit panel")
	}
	for _, f := range phaseToolFields(MachinePhase{Name: "think", Reach: ReachRead}, editorCatalog{}) {
		if f.Type == "header" && strings.Contains(f.Label, "Kept, not applied") {
			t.Error("a model step's limits are in effect and must not say otherwise")
		}
	}
}
