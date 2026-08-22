package sandbox

import (
	"strings"
	"testing"
)

// A missing helper reaches the model as ModuleNotFoundError, which names
// the script's import and not the deployment that failed. The model
// cannot tell those apart, so it reports an unspecified environmental
// fault and routes around the tool — the expensive half.
func TestMissingGohortModuleExplainsItself(t *testing.T) {
	raw := "Traceback (most recent call last):\n  File \"tool.py\", line 1\n" +
		"ModuleNotFoundError: No module named 'gohort'\n"
	got := explainMissingGohortModule(raw, true)
	if got == raw {
		t.Fatal("the failure was passed through unexplained")
	}
	if !strings.Contains(got, raw) {
		t.Error("the original traceback should survive — it is what a person greps for")
	}
	// It must say the two things the model needs to decide what to do:
	// that arguments are irrelevant, and that inventing the answer is not
	// the fallback.
	for _, want := range []string{"DEPLOYMENT fault", "no retry", "not work around it"} {
		if !strings.Contains(got, want) {
			t.Errorf("the note should contain %q: %s", want, got)
		}
	}
	// Unrelated output is untouched — this runs on every sandboxed call.
	clean := "hello\nModuleNotFoundError: No module named 'requests'\n"
	if explainMissingGohortModule(clean, true) != clean {
		t.Error("a different missing module must not be explained as this one")
	}
}
