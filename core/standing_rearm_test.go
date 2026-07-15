package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// withSchedulerDB points the package scheduler DB at an in-memory store for
// the duration of a test, so ScheduleTask / ListScheduledTasks / re-arm work.
func withSchedulerDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	schedDBMu.Lock()
	prev := schedDB
	schedDB = db
	schedDBMu.Unlock()
	t.Cleanup(func() {
		schedDBMu.Lock()
		schedDB = prev
		schedDBMu.Unlock()
	})
	return db
}

// TestRearmStrandedStandingAgents: an active agent whose NextRun is frozen in
// the past with no live task gets rescheduled to a FUTURE time; paused agents,
// agents with a live task, and agents whose next fire is imminent are left
// alone.
func TestRearmStrandedStandingAgents(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	now := time.Now()

	// Stranded: active, NextRun 9 days ago, no scheduler task.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "wiwee", Cron: "daily 12:00",
		NextRun: now.Add(-9 * 24 * time.Hour), SchedulerID: "dead-task-id",
	})
	// Paused: must never be re-armed even though NextRun is stale.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "paused-job", Cron: "daily 09:00",
		NextRun: now.Add(-9 * 24 * time.Hour), Paused: true,
	})
	// Healthy: NextRun in the future — leave it.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "healthy", Cron: "daily 08:00",
		NextRun: now.Add(6 * time.Hour),
	})

	revived := RearmStrandedStandingAgents(db)
	if revived != 1 {
		t.Fatalf("expected exactly 1 revived, got %d", revived)
	}

	// The stranded one now points at a FUTURE fire and has a fresh task id.
	sa, _ := GetStandingAgent(db, "u", "wiwee")
	if !sa.NextRun.After(now) {
		t.Fatalf("stranded agent NextRun not advanced to the future: %s", sa.NextRun)
	}
	if sa.NextRun.Before(now) {
		t.Fatal("missed runs must not be backfilled — NextRun must be future")
	}
	if sa.SchedulerID == "dead-task-id" || sa.SchedulerID == "" {
		t.Fatalf("stranded agent should have a fresh scheduler id, got %q", sa.SchedulerID)
	}

	// Paused stayed paused and unscheduled.
	if p, _ := GetStandingAgent(db, "u", "paused-job"); p.SchedulerID != "" {
		t.Error("paused agent must not be re-armed")
	}

	// A second sweep is a no-op now that everything has a live task.
	if again := RearmStrandedStandingAgents(db); again != 0 {
		t.Fatalf("second sweep should revive nothing, got %d", again)
	}
}
