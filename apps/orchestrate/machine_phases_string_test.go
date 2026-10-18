package orchestrate

import (
	"strings"
	"testing"
)

// Phases sent as a JSON string are still the phases. A deferred machine tool
// called directly reaches the parser without the agent loop's decode, and
// refusing the string there cost one build fourteen identical failures.
func TestMachinePhasesAcceptAJSONString(t *testing.T) {
	phases, err := parseMachinePhases(`[{"name": "probe", "desc": "test", "prompt": "Reply briefly.", "resident": true}]`)
	if err != nil || len(phases) != 1 || phases[0].Name != "probe" || !phases[0].Resident {
		t.Fatalf("phases from a JSON string: %+v, %v", phases, err)
	}
	if _, err := parseMachinePhases(`[{"name": "probe"`); err == nil || !strings.Contains(err.Error(), "not a JSON array") {
		t.Errorf("a broken string should say what was wrong with it: %v", err)
	}
	if _, err := parseMachinePhases(42); err == nil || !strings.Contains(err.Error(), "float64") && !strings.Contains(err.Error(), "int") {
		t.Errorf("a wrong type should say what arrived: %v", err)
	}
}
