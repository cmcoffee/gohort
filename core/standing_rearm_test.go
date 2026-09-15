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

// A standing agent's next occurrence does not exist while its fire runs: the
// re-arm is deferred and re-reads the record. So an attempt that asks to come
// back later just writes the time down, and arming has to honour it — for that
// one occurrence, and then go back to the cadence (docs/objective-pacing.md).
func TestScheduleStandingAgentHonoursAPacedAttempt(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	paced := time.Now().Add(5 * time.Hour).Truncate(time.Second)

	sa := StandingAgent{
		Owner: "u", Name: "release-notes", IntervalSeconds: 3600,
		Until:          "the notes are published",
		NextAttemptAt:  paced,
		NextAttemptWhy: "the build is still running",
	}
	if err := ScheduleStandingAgent(db, sa); err != nil {
		t.Fatalf("ScheduleStandingAgent: %v", err)
	}

	got, ok := GetStandingAgent(db, "u", "release-notes")
	if !ok {
		t.Fatal("the record vanished")
	}
	if !got.NextRun.Equal(paced) {
		t.Fatalf("armed for %s, but the attempt asked for %s", got.NextRun, paced)
	}
	if !got.NextAttemptAt.IsZero() {
		t.Error("the ask was not consumed, so it would move every future occurrence too")
	}
	if got.NextAttemptWhy != "the build is still running" {
		t.Error("the reason must outlive the ask by one occurrence — it explains the run now armed")
	}

	// The arming AFTER that one is back on the cadence, and the reason goes
	// with the occurrence it explained.
	if err := ScheduleStandingAgent(db, got); err != nil {
		t.Fatalf("second arm: %v", err)
	}
	got2, _ := GetStandingAgent(db, "u", "release-notes")
	if got2.NextRun.Equal(paced) {
		t.Error("the second arm reused the paced time; pacing moves ONE occurrence")
	}
	if diff := got2.NextRun.Sub(time.Now()); diff > 61*time.Minute || diff < 59*time.Minute {
		t.Errorf("second arm is %s out, wanted the hourly cadence", diff)
	}
	if got2.NextAttemptWhy != "" {
		t.Error("a stale reason would claim the cadence's own time was chosen by an attempt")
	}
}

// A time that has already passed is not a schedule. It can happen when a fire
// outlives the wait it asked for, and the answer is the cadence, not a fire in
// the past.
func TestAPacedTimeInThePastIsIgnored(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}

	sa := StandingAgent{
		Owner: "u", Name: "stale", IntervalSeconds: 600,
		NextAttemptAt:  time.Now().Add(-2 * time.Hour),
		NextAttemptWhy: "waiting on something that already happened",
	}
	if err := ScheduleStandingAgent(db, sa); err != nil {
		t.Fatalf("ScheduleStandingAgent: %v", err)
	}
	got, _ := GetStandingAgent(db, "u", "stale")
	if !got.NextRun.After(time.Now()) {
		t.Fatalf("armed in the past: %s", got.NextRun)
	}
	if !got.NextAttemptAt.IsZero() || got.NextAttemptWhy != "" {
		t.Error("a stale ask must be cleared, not left to be reconsidered every arm")
	}
}
