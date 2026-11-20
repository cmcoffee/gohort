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

// registerKind registers fn for kind for one test, self-recovering or not.
func registerKind(t *testing.T, kind string, self bool, fn ScheduleHandlerFunc) {
	t.Helper()
	if self {
		RegisterSelfRecoveringScheduleHandler(kind, fn)
	} else {
		RegisterScheduleHandler(kind, fn)
	}
	t.Cleanup(func() {
		schedHandlersMu.Lock()
		delete(schedHandlers, kind)
		delete(schedSelfRecovering, kind)
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

// A run is on record while it runs and off it once it ends; a stop cut it
// off when it is still there at the next start, which queues it again a
// little later, once.
func TestARunCutOffByAStopRunsAgainOnce(t *testing.T) {
	db := useSchedDB(t)
	release := make(chan struct{})
	started := make(chan json.RawMessage, 4)
	registerKind(t, "test.resumable", false, func(ctx context.Context, p json.RawMessage) {
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
		t.Fatal("a run is not on record while it runs")
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
// left on it, since the stop cut it off. A self-recovering kind keeps no
// record at all.
func TestOnlyACutOffRunStaysOnRecord(t *testing.T) {
	db := useSchedDB(t)
	done := make(chan struct{}, 4)
	registerKind(t, "test.ends", false, func(ctx context.Context, p json.RawMessage) { done <- struct{}{} })
	registerKind(t, "test.panics", false, func(ctx context.Context, p json.RawMessage) { done <- struct{}{}; panic("boom") })
	registerKind(t, "test.self", true, func(ctx context.Context, p json.RawMessage) { done <- struct{}{} })

	ends, _ := ScheduleTask("test.ends", nil, time.Now().Add(-time.Second))
	panics, _ := ScheduleTask("test.panics", nil, time.Now().Add(-time.Second))
	fireDueTasks(context.Background(), db)
	<-done
	<-done
	schedWaitFor(t, "the ended runs to leave the record", func() bool { return !schedRunning(db, ends) && !schedRunning(db, panics) })

	ctx, cancel := context.WithCancel(context.Background())
	stopping := make(chan struct{})
	registerKind(t, "test.stopping", false, func(c context.Context, p json.RawMessage) {
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

	self, _ := ScheduleTask("test.self", nil, time.Now().Add(-time.Second))
	schedHandlersMu.Lock()
	schedHandlers["test.self"] = func(ctx context.Context, p json.RawMessage) {
		if schedRunning(db, self) {
			t.Error("a self-recovering kind was put on the record")
		}
		done <- struct{}{}
	}
	schedHandlersMu.Unlock()
	fireDueTasks(context.Background(), db)
	<-done
}

// Registering a kind plainly after it was registered self-recovering makes
// it plain: the last registration says what the kind is.
func TestAPlainRegistrationIsNotSelfRecovering(t *testing.T) {
	fn := func(ctx context.Context, p json.RawMessage) {}
	registerKind(t, "test.reregistered", true, fn)
	registerKind(t, "test.reregistered", false, fn)
	schedHandlersMu.RLock()
	self := schedSelfRecovering["test.reregistered"]
	schedHandlersMu.RUnlock()
	if self {
		t.Error("still self-recovering after a plain registration")
	}
}

// The scheduler's first pass queues the runs a stop cut off again before
// firing what came due: a cut-off run is back in the queue, not run at once.
func TestTheFirstPassQueuesCutOffRunsAgain(t *testing.T) {
	db := useSchedDB(t)
	fired := make(chan struct{}, 2)
	registerKind(t, "test.startup", false, func(ctx context.Context, p json.RawMessage) { fired <- struct{}{} })
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

// queueAt puts a task of kind with payload in the queue, due at at.
func queueAt(t *testing.T, db Database, id, kind, payload string, at time.Time) {
	t.Helper()
	schedDBMu.Lock()
	db.Set(schedulerTable, id, ScheduledTask{ID: id, Kind: kind, Payload: json.RawMessage(payload), RunAt: at.UTC().Format(time.RFC3339)})
	schedDBMu.Unlock()
}

// cutOff leaves a run of kind with payload on the running record, as a stop
// under it does.
func cutOff(db Database, id, kind, payload string) {
	schedDBMu.Lock()
	db.Set(schedulerRunningTable, id, ScheduledTask{ID: id, Kind: kind, Payload: json.RawMessage(payload)})
	schedDBMu.Unlock()
}

// A cut-off run is left to its job's next run when that is due within the
// hour, an overdue one included (it fires in the same first pass), and runs
// again when the next run is further off, or is another job's.
func TestACutOffRunIsLeftToANextRunDueSoon(t *testing.T) {
	db := useSchedDB(t)
	now := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name, payload string
		at            time.Time
		again         bool
	}{
		{"hourly", `{"job":"a"}`, now.Add(40 * time.Minute), false},
		{"at the hour", `{"job":"a"}`, now.Add(resumeSkipWithin), false},
		{"overdue", `{"job":"a"}`, now.Add(-3 * time.Hour), false},
		{"spaced apart", `{ "job": "a" }`, now.Add(10 * time.Minute), false},
		{"daily", `{"job":"a"}`, now.Add(23 * time.Hour), true},
		{"every 90 minutes", `{"job":"a"}`, now.Add(90 * time.Minute), true},
		{"just past the hour", `{"job":"a"}`, now.Add(resumeSkipWithin + time.Second), true},
		{"another job", `{"job":"b"}`, now.Add(10 * time.Minute), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			schedDBMu.Lock()
			for _, k := range db.Keys(schedulerTable) {
				db.Unset(schedulerTable, k)
			}
			schedDBMu.Unlock()
			queueAt(t, db, "next", "test.job", c.payload, c.at)
			cutOff(db, "cut", "test.job", `{"job":"a"}`)
			resumeInterrupted(db, now)
			_, queued := schedQueued(db, "cut")
			if queued != c.again {
				t.Errorf("run again = %v, want %v", queued, c.again)
			}
			if schedRunning(db, "cut") {
				t.Error("the record of the cut-off run was kept")
			}
		})
	}

	// The same payload under another kind is another job.
	schedDBMu.Lock()
	for _, k := range db.Keys(schedulerTable) {
		db.Unset(schedulerTable, k)
	}
	schedDBMu.Unlock()
	queueAt(t, db, "next", "test.other", `{"job":"a"}`, now.Add(10*time.Minute))
	cutOff(db, "cut", "test.job", `{"job":"a"}`)
	resumeInterrupted(db, now)
	if _, ok := schedQueued(db, "cut"); !ok {
		t.Error("a run of another kind with the same payload stopped the run again")
	}
}

// A run done again checks once more as it comes due: a chain whose next run
// was lost with the stop is armed again by its reconciler after the start,
// and a next run due within the hour by then takes its place.
func TestARunDoneAgainIsDroppedWhenItsNextRunIsArmedSoon(t *testing.T) {
	db := useSchedDB(t)
	fired := make(chan string, 4)
	registerKind(t, "test.chain", false, func(ctx context.Context, p json.RawMessage) { fired <- string(p) })
	past := time.Now().Add(-time.Second)
	schedDBMu.Lock()
	db.Set(schedulerTable, "again", ScheduledTask{ID: "again", Kind: "test.chain", Payload: json.RawMessage(`{"m":1}`), RunAt: past.UTC().Format(time.RFC3339), Resumes: 1})
	schedDBMu.Unlock()
	queueAt(t, db, "armed", "test.chain", `{"m":1}`, time.Now().Add(30*time.Minute))
	fireDueTasks(context.Background(), db)
	if _, ok := schedQueued(db, "again"); ok {
		t.Error("the run done again stayed in the queue")
	}
	if schedRunning(db, "again") {
		t.Error("a dropped run was put on the running record")
	}
	select {
	case p := <-fired:
		t.Errorf("the run done again fired (%s) with its next run due in 30 minutes", p)
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := schedQueued(db, "armed"); !ok {
		t.Error("the next run was taken out of the queue")
	}

	// Due in the same pass as its next run: still the next run's place.
	schedDBMu.Lock()
	db.Set(schedulerTable, "again2", ScheduledTask{ID: "again2", Kind: "test.chain", Payload: json.RawMessage(`{"m":2}`), RunAt: past.UTC().Format(time.RFC3339), Resumes: 1})
	schedDBMu.Unlock()
	queueAt(t, db, "armed2", "test.chain", `{"m":2}`, past)
	fireDueTasks(context.Background(), db)
	if got := <-fired; got != `{"m":2}` {
		t.Errorf("fired %s", got)
	}
	select {
	case p := <-fired:
		t.Errorf("both the run done again and its next run fired (%s)", p)
	case <-time.After(20 * time.Millisecond):
	}

	// Its next run further off: it runs.
	schedDBMu.Lock()
	db.Set(schedulerTable, "again3", ScheduledTask{ID: "again3", Kind: "test.chain", Payload: json.RawMessage(`{"m":3}`), RunAt: past.UTC().Format(time.RFC3339), Resumes: 1})
	schedDBMu.Unlock()
	queueAt(t, db, "armed3", "test.chain", `{"m":3}`, time.Now().Add(20*time.Hour))
	fireDueTasks(context.Background(), db)
	select {
	case p := <-fired:
		if p != `{"m":3}` {
			t.Errorf("fired %s", p)
		}
	case <-time.After(time.Second):
		t.Error("a run done again with its next run 20 hours off did not fire")
	}

	// A first run is never dropped for a sibling: only a run done again is.
	queueAt(t, db, "first", "test.chain", `{"m":4}`, past)
	queueAt(t, db, "armed4", "test.chain", `{"m":4}`, time.Now().Add(10*time.Minute))
	fireDueTasks(context.Background(), db)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Error("a first run was dropped because the same job was queued again soon")
	}
}

