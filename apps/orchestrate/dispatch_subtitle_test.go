package orchestrate

import (
	"strings"
	"testing"
)

// The subtitle has to name the CURRENT policy and say where to change it. It
// pointed at a "Cortex & delegation" accordion in the agent editor long after
// the policy had moved to the Security page, right above this list.
func TestDispatchTargetSubtitle(t *testing.T) {
	for _, mode := range []string{dispatchAll, dispatchOnly, dispatchExcept, dispatchNone} {
		got := dispatchTargetSubtitle(mode)
		if !strings.Contains(got, "Which agents it can call at all") || strings.Contains(got, "Cortex & delegation") {
			t.Errorf("%s: subtitle must say where the policy lives: %s", mode, got)
		}
		if !strings.Contains(got, "Currently") {
			t.Errorf("%s: subtitle must state the current policy: %s", mode, got)
		}
	}
	// The two modes that ignore the list must say so plainly.
	for _, mode := range []string{dispatchAll, dispatchNone} {
		if !strings.Contains(dispatchTargetSubtitle(mode), "no effect") {
			t.Errorf("%s: should tell the reader the list is inert", mode)
		}
	}
}
