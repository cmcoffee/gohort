package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// useSchedDB points the scheduler at a fresh store for one test.
func useSchedDB(t *testing.T) Database {
	t.Helper()
	db := memDB(t)
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

// registerKind registers fn for kind for one test, resumable or not.
func registerKind(t *testing.T, kind string, resumable bool, fn ScheduleHandlerFunc) {
	t.Helper()
	if resumable {
		RegisterResumableScheduleHandler(kind, fn)
	} else {
		RegisterScheduleHandler(kind, fn)
	}
	t.Cleanup(func() {
		schedHandlersMu.Lock()
		delete(schedHandlers, kind)
		delete(schedResumable, kind)
		schedHandlersMu.Unlock()
	})
}

// schedRunning reports whether task id is on the running record.
func schedRunning(db Database, id string) bool {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	var task ScheduledTask
	return db.Get(schedulerRunningTable, id, &task)
}

// schedQueued is task id as queued, and whether it is.
func schedQueued(db Database, id string) (ScheduledTask, bool) {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	var task ScheduledTask
	ok := db.Get(schedulerTable, id, &task)
	return task, ok
}

// schedWaitFor fails the test when cond is not true within a second.
func schedWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A resumable kind's run is on record while it runs and off it once it
// ends; a stop cut it off when it is still there at the next start, which
// queues it again a little later, once.
func TestARunCutOffByAStopRunsAgainOnce(t *testing.T) {
	db := useSchedDB(t)
	release := make(chan struct{})
	started := make(chan json.RawMessage, 4)
	registerKind(t, "test.resumable", true, func(ctx context.Context, p json.RawMessage) {
		started <- p
		<-release
	})
	id, err := ScheduleTask("test.resumable", map[string]string{"day": "today"}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	fireDueTasks(context.Background(), db)
	<-started
	if !schedRunning(db, id) {
		t.Fatal("a resumable run is not on record while it runs")
	}

	// The process stops here: what a start finds is the record.
	now := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	resumeInterrupted(db, now)
	if schedRunning(db, id) {
		t.Error("the record of a cut-off run was kept after it was queued again")
	}
	task, ok := schedQueued(db, id)
	if !ok || task.Resumes != 1 || task.RunAt != now.Add(resumeDelay).Format(time.RFC3339) || string(task.Payload) != `{"day":"today"}` {
		t.Fatalf("the cut-off run was not queued again as it was, %v later: %+v (%v)", resumeDelay, task, ok)
	}

	// Cut off again: not run a third time.
	schedDBMu.Lock()
	db.Unset(schedulerTable, id)
	db.Set(schedulerRunningTable, id, task)
	schedDBMu.Unlock()
	resumeInterrupted(db, now)
	if _, ok := schedQueued(db, id); ok || schedRunning(db, id) {
		t.Error("a run cut off after being run again was queued a third time")
	}
	close(release)
}

// A run that ends is taken off the record, a panic included (run again, it
// would panic again); one that ends because the process is stopping is
// left on it, since the stop cut it off. A kind registered plainly keeps
// no record at all.
func TestOnlyACutOffRunStaysOnRecord(t *testing.T) {
	db := useSchedDB(t)
	done := make(chan struct{}, 4)
	registerKind(t, "test.ends", true, func(ctx context.Context, p json.RawMessage) { done <- struct{}{} })
	registerKind(t, "test.panics", true, func(ctx context.Context, p json.RawMessage) { done <- struct{}{}; panic("boom") })
	registerKind(t, "test.plain", false, func(ctx context.Context, p json.RawMessage) { done <- struct{}{} })

	ends, _ := ScheduleTask("test.ends", nil, time.Now().Add(-time.Second))
	panics, _ := ScheduleTask("test.panics", nil, time.Now().Add(-time.Second))
	fireDueTasks(context.Background(), db)
	<-done
	<-done
	schedWaitFor(t, "the ended runs to leave the record", func() bool { return !schedRunning(db, ends) && !schedRunning(db, panics) })

	ctx, cancel := context.WithCancel(context.Background())
	stopping := make(chan struct{})
	registerKind(t, "test.stopping", true, func(c context.Context, p json.RawMessage) {
		cancel()
		close(stopping)
	})
	cut, _ := ScheduleTask("test.stopping", nil, time.Now().Add(-time.Second))
	fireDueTasks(ctx, db)
	<-stopping
	time.Sleep(20 * time.Millisecond)
	if !schedRunning(db, cut) {
		t.Error("a run that ended as the process stopped was taken off the record")
	}

	plain, _ := ScheduleTask("test.plain", nil, time.Now().Add(-time.Second))
	schedHandlersMu.Lock()
	schedHandlers["test.plain"] = func(ctx context.Context, p json.RawMessage) {
		if schedRunning(db, plain) {
			t.Error("a kind registered plainly was put on the record")
		}
		done <- struct{}{}
	}
	schedHandlersMu.Unlock()
	fireDueTasks(context.Background(), db)
	<-done
}

// Registering a kind plainly after it was registered resumable makes it
// plain: the last registration says what the kind is.
func TestAPlainRegistrationIsNotResumable(t *testing.T) {
	fn := func(ctx context.Context, p json.RawMessage) {}
	registerKind(t, "test.reregistered", true, fn)
	registerKind(t, "test.reregistered", false, fn)
	schedHandlersMu.RLock()
	resumable := schedResumable["test.reregistered"]
	schedHandlersMu.RUnlock()
	if resumable {
		t.Error("still resumable after a plain registration")
	}
}

// The scheduler's first pass queues the runs a stop cut off again before
// firing what came due: a cut-off run is back in the queue, not run at once.
func TestTheFirstPassQueuesCutOffRunsAgain(t *testing.T) {
	db := useSchedDB(t)
	fired := make(chan struct{}, 2)
	registerKind(t, "test.startup", true, func(ctx context.Context, p json.RawMessage) { fired <- struct{}{} })
	task := ScheduledTask{ID: "cut-1", Kind: "test.startup", RunAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	schedDBMu.Lock()
	db.Set(schedulerRunningTable, task.ID, task)
	schedDBMu.Unlock()
	startupFire(context.Background(), db)
	got, ok := schedQueued(db, "cut-1")
	if !ok || got.Resumes != 1 {
		t.Fatalf("the first pass did not queue the cut-off run again: %+v %v", got, ok)
	}
	select {
	case <-fired:
		t.Error("the cut-off run fired at once rather than after the delay")
	case <-time.After(20 * time.Millisecond):
	}
}
