package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestRearmStrandedEventMonitors mirrors the standing-agent case: a stranded
// active scheduled monitor is rescheduled to a future check; paused, healthy,
// and non-scheduled (webhook) monitors are left alone.
func TestRearmStrandedEventMonitors(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	now := time.Now()

	// Stranded: active poll monitor, NextCheck long past, no live task.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "disk-watch", Kind: EventKindPoll, IntervalSeconds: 300,
		NextCheck: now.Add(-3 * time.Hour), SchedulerID: "dead-task",
	})
	// Paused: never re-arm.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "paused-watch", Kind: EventKindPoll, IntervalSeconds: 300,
		NextCheck: now.Add(-3 * time.Hour), Paused: true,
	})
	// Healthy: NextCheck in the future.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "healthy-watch", Kind: EventKindHTTP, IntervalSeconds: 600,
		NextCheck: now.Add(10 * time.Minute),
	})
	// Webhook: not a scheduled kind — nothing to re-arm.
	SaveEventMonitor(db, EventMonitor{
		Owner: "u", Name: "hook", Kind: EventKindWebhook,
	})

	revived := RearmStrandedEventMonitors(db)
	if revived != 1 {
		t.Fatalf("expected exactly 1 revived, got %d", revived)
	}

	m, _ := GetEventMonitor(db, "u", "disk-watch")
	if !m.NextCheck.After(now) {
		t.Fatalf("stranded monitor NextCheck not advanced to the future: %s", m.NextCheck)
	}
	if m.SchedulerID == "dead-task" || m.SchedulerID == "" {
		t.Fatalf("stranded monitor should have a fresh scheduler id, got %q", m.SchedulerID)
	}
	if p, _ := GetEventMonitor(db, "u", "paused-watch"); p.SchedulerID != "" {
		t.Error("paused monitor must not be re-armed")
	}

	if again := RearmStrandedEventMonitors(db); again != 0 {
		t.Fatalf("second sweep should revive nothing, got %d", again)
	}
}
