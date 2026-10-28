package extensions

import (
	"strings"
	"testing"
)

// A slow build names each step and its time; one not being timed is a no-op.
func TestPhaseTimesNameEachStep(t *testing.T) {
	pt := startPhases()
	pt.mark("tools")
	pt.mark("render")
	if len(pt.steps) != 2 || !strings.HasPrefix(pt.steps[0], "tools ") || !strings.HasPrefix(pt.steps[1], "render ") {
		t.Errorf("steps = %q", pt.steps)
	}
	var none *phaseTimes
	none.mark("x")
	none.logIfSlow("nothing")
}
