package filestore

import "testing"

// The input label is what the box asks for on a second phase, so it shows
// only for a two-phase command.
func TestTheInputLabelIsOnlyForTwoPhases(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range actionFormFields() {
		seen[f.Field] = true
		if f.Field == "input_label" && f.ShowWhen != "two_phase" {
			t.Errorf("input_label ShowWhen = %q, want %q", f.ShowWhen, "two_phase")
		}
	}
	for _, f := range []string{"two_phase", "input_label"} {
		if !seen[f] {
			t.Errorf("the %s field is gone", f)
		}
	}
}
