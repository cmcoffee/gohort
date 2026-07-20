package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestRearmStrandedScheduledTriggers mirrors the monitor/standing cases: a
// stranded active recurring trigger is rescheduled to a future fire; paused,
// push, one-shot, and healthy (live-task) triggers are left alone.
func TestRearmStrandedScheduledTriggers(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	now := time.Now()

	// Stranded: active recurring trigger, NextRun long past, no live task.
	SaveScheduledTrigger(db, ScheduledTrigger{
		Owner: "u", Name: "stranded", IntervalSeconds: 300,
		NextRun: now.Add(-3 * time.Hour), SchedulerID: "dead-task",
	})
	// Paused: never re-arm.
	SaveScheduledTrigger(db, ScheduledTrigger{
		Owner: "u", Name: "paused", IntervalSeconds: 300,
		NextRun: now.Add(-3 * time.Hour), Paused: true,
	})
	// Push: no timer at all.
	SaveScheduledTrigger(db, ScheduledTrigger{
		Owner: "u", Name: "pushy", Push: true,
		NextRun: now.Add(-3 * time.Hour),
	})
	// One-shot (no interval/cron/window): at-most-once — never replayed.
	SaveScheduledTrigger(db, ScheduledTrigger{
		Owner: "u", Name: "oneshot",
		RunAt: now.Add(-3 * time.Hour), NextRun: now.Add(-3 * time.Hour), SchedulerID: "dead-task",
	})
	// Healthy: stale-looking NextRun but a LIVE queued task — mid-window churn
	// must not double-arm it.
	if _, err := ScheduleTask(triggerKind, triggerPayload{Owner: "u", Name: "healthy"}, now.Add(time.Hour)); err != nil {
		t.Fatalf("ScheduleTask: %v", err)
	}
	SaveScheduledTrigger(db, ScheduledTrigger{
		Owner: "u", Name: "healthy", IntervalSeconds: 300,
		NextRun: now.Add(-3 * time.Hour),
	})

	revived := RearmStrandedScheduledTriggers(db)
	if revived != 1 {
		t.Fatalf("expected exactly 1 revived, got %d", revived)
	}
	cur, ok := GetScheduledTrigger(db, "u", "stranded")
	if !ok || cur.NextRun.Before(now) || cur.SchedulerID == "" || cur.SchedulerID == "dead-task" {
		t.Fatalf("stranded trigger not re-armed: %+v", cur)
	}
	if one, _ := GetScheduledTrigger(db, "u", "oneshot"); one.SchedulerID != "dead-task" {
		t.Fatalf("one-shot must not be replayed: %+v", one)
	}

	// Idempotent: the revived trigger now holds a live task.
	if again := RearmStrandedScheduledTriggers(db); again != 0 {
		t.Fatalf("second pass revived %d, want 0", again)
	}
}
