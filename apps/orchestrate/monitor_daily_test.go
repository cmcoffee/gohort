package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The Scheduler's monitor editor switches a monitor to set times and back,
// refuses set times on a push-triggered one, and a change of times re-arms it.
func TestAMonitorCanRunAtSetTimes(t *testing.T) {
	m := EventMonitor{Kind: EventKindWatch, IntervalSeconds: 900}
	at := "08:00,18:30"
	if err := applyMonitorUpdate(&m, monitorUpdateBody{DailyAt: &at}); err != nil {
		t.Fatal(err)
	}
	if len(m.DailyAt) != 2 || monitorCadence(m) != "daily at 08:00, 18:30" {
		t.Errorf("set times: %v %q", m.DailyAt, monitorCadence(m))
	}
	before := m
	before.DailyAt = []int{8 * 60}
	if !monitorNeedsRearm(before, m) {
		t.Error("changing the times re-arms the monitor")
	}
	clear := ""
	if err := applyMonitorUpdate(&m, monitorUpdateBody{DailyAt: &clear, IntervalSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	if m.DailyAt != nil || monitorCadence(m) != "every 600s" {
		t.Errorf("back to an interval: %v %q", m.DailyAt, monitorCadence(m))
	}
	hook := EventMonitor{Kind: EventKindWebhook}
	if err := applyMonitorUpdate(&hook, monitorUpdateBody{DailyAt: &at}); err == nil {
		t.Error("a push-triggered monitor has no schedule for set times")
	}
}
