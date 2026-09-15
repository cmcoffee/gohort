package monitor

import (
	"strings"
	"testing"
)

// The Cancel button belongs on the rows that HAVE a cancel behind them, not on
// every row that happens to be running. Those were not the same thing: most
// live runs were created with no cancel func, so the button appeared, the POST
// answered success, and the work carried on. The activity endpoint sends
// _cancellable now; this is the half that reads it.
func TestCancelIsOfferedOnlyWhereItWorks(t *testing.T) {
	agents := monitorAgentsTable("/orchestrate/api/console/activity")

	var found bool
	for _, a := range agents.RowActions {
		if a.Label != "Cancel" {
			continue
		}
		found = true
		if a.OnlyIf != "_cancellable" {
			t.Errorf("Cancel is gated on %q; it must be _cancellable, or it appears on runs nothing can stop", a.OnlyIf)
		}
		if !strings.Contains(a.PostTo, "/activity/cancel") {
			t.Errorf("Cancel posts to %q", a.PostTo)
		}
		if strings.TrimSpace(a.Confirm) == "" {
			t.Error("stopping work mid-turn should confirm first")
		}
	}
	if !found {
		t.Fatal("the Cancel row action is gone from the Monitor page")
	}
}
