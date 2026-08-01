package ui

import (
	"os"
	"strings"
	"testing"
)

var osReadFile = os.ReadFile

// Every component that re-fetches on an interval used to write its own bare
// setInterval. All three copies shared the same three faults, and the worst one
// costs money: a source_script panel's "poll" runs a sandboxed script on the
// server, sometimes with an outbound fetch behind it, so a dashboard left open
// in a background tab spends real budget for as long as the tab lives.
func TestAutoRefreshIsOnePollerWithTheThreeGuards(t *testing.T) {
	prelude := mustRuntimePart(t, "00_prelude.js")
	if !strings.Contains(prelude, "function uiAutoRefresh(") {
		t.Fatal("the shared poller is gone; each renderer is writing its own interval again")
	}
	for _, guard := range []struct{ src, why string }{
		{"document.hidden", "a hidden tab must not poll — that is server work nobody is looking at"},
		{"busy", "a slow source must not stack overlapping requests"},
		{"visibilitychange", "returning to a tab must refresh now, not after another full interval"},
		{"clearInterval", "the poller has to be stoppable"},
	} {
		if !strings.Contains(prelude, guard.src) {
			t.Errorf("uiAutoRefresh lost %q: %s", guard.src, guard.why)
		}
	}

	// And the renderers use it rather than rolling their own again.
	basics := mustRuntimePart(t, "10_basics.js")
	if n := strings.Count(basics, "uiAutoRefresh(cfg.auto_refresh_ms"); n < 3 {
		t.Errorf("want table, display and chart on the shared poller; found %d call(s)", n)
	}
	if strings.Contains(basics, "setInterval(reload") || strings.Contains(basics, "setInterval(function(){ reload") {
		t.Error("a bare setInterval is back — it will poll a hidden tab forever")
	}
}

// A chart of live data is the case auto-refresh exists for, and this was the
// one source-backed component that could not do it at all.
func TestChartCanRefreshAndReactToRecordWrites(t *testing.T) {
	basics := mustRuntimePart(t, "10_basics.js")
	chart := basics[strings.Index(basics, "components.chart_panel"):]
	chart = chart[:strings.Index(chart, "components.action_list")]
	if !strings.Contains(chart, "uiAutoRefresh(cfg.auto_refresh_ms") {
		t.Error("a source-backed chart must be able to poll")
	}
	if !strings.Contains(chart, "ui-data-changed") {
		t.Error("a chart computed FROM records must refresh when a record is written")
	}
	// The Go side has to offer the field, or none of the above is reachable.
	if !strings.Contains(runtimeGoSource(t), "AutoRefreshMS") {
		t.Error("ChartPanel.AutoRefreshMS is missing; the runtime support is unreachable from Go")
	}
}

func mustFile(t *testing.T, name string) string {
	t.Helper()
	b, err := osReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func runtimeGoSource(t *testing.T) string {
	t.Helper()
	src := mustFile(t, "components.go")
	i := strings.Index(src, "type ChartPanel struct")
	if i < 0 {
		t.Fatal("ChartPanel is gone")
	}
	return src[i : i+1200]
}
