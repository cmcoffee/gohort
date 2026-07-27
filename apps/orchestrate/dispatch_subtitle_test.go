package orchestrate

import (
	"strings"
	"testing"
)

// The subtitle has to name the CURRENT policy and say where to change
// it — it used to say "the Dispatch policy above", pointing at a select
// inside a collapsed accordion in a different widget.
func TestDispatchTargetSubtitle(t *testing.T) {
	for _, mode := range []string{dispatchAll, dispatchOnly, dispatchExcept, dispatchNone} {
		got := dispatchTargetSubtitle(mode)
		if !strings.Contains(got, "Cortex & delegation") {
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
