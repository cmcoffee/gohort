package monitor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
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

// Every row opens its run's trace, live: the trace endpoint for THAT row, a
// refresh a person watching can see move, and a spinner on what is running.
func TestEveryRunOpensItsTrace(t *testing.T) {
	agents := monitorAgentsTable("/orchestrate/api/console/activity")
	var trace *ui.RowAction
	for i, a := range agents.RowActions {
		if a.Label == "Trace" {
			trace = &agents.RowActions[i]
		}
	}
	if trace == nil {
		t.Fatal("the Monitor rows have no Trace")
	}
	if trace.Type != "modal" || trace.OnlyIf != "" {
		t.Errorf("Trace is a dialog on every row, finished ones too: type %q, only if %q", trace.Type, trace.OnlyIf)
	}
	var tbl struct {
		Type string `json:"type"`
		Cfg  struct {
			Source  string `json:"source"`
			Refresh int    `json:"auto_refresh_ms"`
			Columns []struct {
				Field  string `json:"field"`
				Badges []struct {
					Value any  `json:"value"`
					Spin  bool `json:"spin"`
				} `json:"badges"`
			} `json:"columns"`
		}
	}
	if err := json.Unmarshal(trace.Render, &tbl.Cfg); err != nil {
		t.Fatalf("render: %v\n%s", err, trace.Render)
	}
	if tbl.Cfg.Source != "/orchestrate/api/console/trace?id={_id}" {
		t.Errorf("the trace of this row's run: %q", tbl.Cfg.Source)
	}
	if tbl.Cfg.Refresh <= 0 || tbl.Cfg.Refresh > 3000 {
		t.Errorf("it refreshes while it goes: %dms", tbl.Cfg.Refresh)
	}
	spins := false
	for _, c := range tbl.Cfg.Columns {
		for _, b := range c.Badges {
			if b.Value == "running" && b.Spin {
				spins = true
			}
		}
	}
	if !spins {
		t.Error("a running step has to move: the running badge spins")
	}
}
