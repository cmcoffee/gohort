package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const schedulerTable = "scheduler_tasks"

// schedulerRunningTable holds tasks while their handler runs: one left there
// at startup was cut off by the process stopping, and runs again unless its
// job is due again soon (resumeSkipWithin). Kinds registered with
// RegisterSelfRecoveringScheduleHandler keep no record here.
const schedulerRunningTable = "scheduler_running"

// resumeDelay is how long after startup an interrupted task runs again:
// long enough for the rest of the server to come up.
const resumeDelay = 2 * time.Minute

// maxResumes is how many times one task is run again after interruptions.
// A run that takes the process down every time is not rerun forever.
const maxResumes = 1

// resumeSkipWithin is how close the same job's next run may be before a
// cut-off run is left to it: an hourly or faster job is not run again, a
// daily one is. The same job is a queued task of the same kind with the same
// payload, which is how every recurring kind arms its next run.
const resumeSkipWithin = time.Hour

// ScheduledTask is a persisted deferred job stored in the global scheduler DB.
type ScheduledTask struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`    // matches a RegisterScheduleHandler key
	Payload json.RawMessage `json:"payload"` // app-defined; passed verbatim to the handler
	RunAt   string          `json:"run_at"`  // RFC3339 UTC
	Created string          `json:"created"`
	// Resumes is how many times the task has been run again after its run
	// was cut off by the process stopping. Zero for every task that never
	// was.
	Resumes int `json:"resumes,omitempty"`
}

// ScheduleHandlerFunc is called when a task becomes due.
// payload is the raw JSON that was passed to ScheduleTask.
type ScheduleHandlerFunc func(ctx context.Context, payload json.RawMessage)

// TaskDescriber turns a task's opaque payload into a short human label for
// admin + log surfaces (e.g. "ts3-join-leave-chat (agent: Chat)"). Registered
// per kind by the domain that OWNS the payload, so the generic scheduler never
// learns any kind's shape — it just asks whoever registered. Return "" when the
// task can't be described (unknown kind, stale/deleted target, malformed payload).
type TaskDescriber func(payload json.RawMessage) string

// RegisterTaskDescriber installs a human-label producer for a task kind. Call
// from an app's RegisterRoutes/init. Optional: kinds without one just show no
// label (the id + kind still render), so this never gates functionality.
func RegisterTaskDescriber(kind string, fn TaskDescriber) {
	taskDescribersMu.Lock()
	taskDescribers[kind] = fn
	taskDescribersMu.Unlock()
}

// DescribeTask returns the registered describer's label for t, or "" when no
// describer is registered for its kind (or the describer panics on a bad row —
// one malformed task must not take down an admin list or a log pass).
func DescribeTask(t ScheduledTask) (label string) {
	taskDescribersMu.RLock()
	fn := taskDescribers[t.Kind]
	taskDescribersMu.RUnlock()
	if fn == nil {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			label = ""
		}
	}()
	return fn(t.Payload)
}

var (
	schedHandlers   = map[string]ScheduleHandlerFunc{}
	schedHandlersMu sync.RWMutex
	// schedSelfRecovering are the kinds that settle their own cut-off runs
	// (RegisterSelfRecoveringScheduleHandler): the scheduler does not run
	// them again. Under schedHandlersMu.
	schedSelfRecovering = map[string]bool{}

	// schedNotify is signalled when tasks are added or removed so the loop
	// can re-evaluate its sleep target without waiting for the current timer.
	schedNotify = make(chan struct{}, 1)

	schedDB   Database
	schedDBMu sync.Mutex

	// postStart is invoked once after the scheduler loop starts,
	// allowing apps to register reconcilers and other one-time setup.
	postStart   func()
	postStartMu sync.Mutex

	// reconcilers are periodic functions that ensure each app's expected
	// tasks are actually scheduled. Run every 30 minutes.
	reconcilers   = map[string]func(context.Context) error{}
	reconcilersMu sync.Mutex

	// taskDescribers turn a task kind's opaque payload into a human label for
	// admin/log surfaces. Keyed by kind, registered by the owning domain.
	taskDescribers   = map[string]TaskDescriber{}
	taskDescribersMu sync.RWMutex
)

// RegisterScheduleHandler registers a handler for the given task kind.
// Call from an app's RegisterRoutes (or init) before StartGlobalScheduler is called.
//
// A run the process stops under (a rebuild, a crash, a shutdown that cancels
// ctx) runs again once, shortly after the next start, unless the same job is
// due again within resumeSkipWithin: a daily post runs again, an hourly poll
// is left to its next run. fn must do its work before it returns; work it
// hands to a goroutine is not covered. A recurring kind must arm its next
// run so that a run done again does not arm a second one (replace the task
// it armed, as the event monitors do). A task due while the server was down
// runs at startup either way.
func RegisterScheduleHandler(kind string, fn ScheduleHandlerFunc) {
	schedHandlersMu.Lock()
	schedHandlers[kind] = fn
	delete(schedSelfRecovering, kind)
	schedHandlersMu.Unlock()
}

// RegisterSelfRecoveringScheduleHandler registers a handler, as
// RegisterScheduleHandler does, for a kind that settles its own runs cut off
// by a stop: the scheduler keeps no record of its runs and never runs one
// again.
func RegisterSelfRecoveringScheduleHandler(kind string, fn ScheduleHandlerFunc) {
	schedHandlersMu.Lock()
	schedHandlers[kind] = fn
	schedSelfRecovering[kind] = true
	schedHandlersMu.Unlock()
}

// ScheduleTask persists a task and wakes the scheduler to re-evaluate timing.
// If the new task's RunAt is earlier than the current sleep target, the scheduler
// will fire it on time; otherwise it continues sleeping to the existing next event.
func ScheduleTask(kind string, payload any, runAt time.Time) (string, error) {
	schedDBMu.Lock()
	db := schedDB
	schedDBMu.Unlock()
	if db == nil {
		return "", fmt.Errorf("global scheduler not started")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := UUIDv4()
	task := ScheduledTask{
		ID:      id,
		Kind:    kind,
		Payload: b,
		RunAt:   runAt.UTC().Format(time.RFC3339),
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	db.Set(schedulerTable, id, task)
	wakeScheduler()
	return id, nil
}

// UpdateScheduledTaskPayload atomically replaces the payload of a STILL-QUEUED
// task, returning false when the task no longer exists (already fired or
// unscheduled). Callers must NOT re-create the task on false — re-adding an
// entry that already fired is how a recurring chain duplicates. Exists for the
// pre-arm pattern: a recurring handler schedules its next occurrence BEFORE
// running a long fire (so a process death mid-fire can't end the chain), then
// updates that occurrence's payload afterward with what the fire learned
// (fire counts, idle-clock renewal). Atomic with fireDueTasks' dequeue under
// schedDBMu.
func UpdateScheduledTaskPayload(id string, payload any) bool {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	if schedDB == nil {
		return false
	}
	var task ScheduledTask
	if !schedDB.Get(schedulerTable, id, &task) {
		return false
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	task.Payload = b
	schedDB.Set(schedulerTable, id, task)
	return true
}

// RescheduleTaskAt moves a STILL-QUEUED task to a new time, returning false
// when the task no longer exists (already fired or unscheduled). Callers must
// NOT re-create the task on false, for the reason UpdateScheduledTaskPayload
// gives: re-adding an entry that already fired is how a recurring chain
// duplicates.
//
// Sibling of UpdateScheduledTaskPayload, one step on. That one exists because
// the pre-arm pattern arms the next occurrence BEFORE a long fire, so whatever
// the fire learns about the next occurrence arrives after the entry exists.
// This is the same problem for the other half of the entry: what the fire
// learns about WHEN the next one should run (docs/design/objective-pacing.md).
//
// Atomic with fireDueTasks' dequeue under schedDBMu, and wakes the loop so a
// task moved EARLIER is not left waiting behind the old sleep target.
func RescheduleTaskAt(id string, runAt time.Time) bool {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	if schedDB == nil {
		return false
	}
	var task ScheduledTask
	if !schedDB.Get(schedulerTable, id, &task) {
		return false
	}
	task.RunAt = runAt.UTC().Format(time.RFC3339)
	schedDB.Set(schedulerTable, id, task)
	wakeScheduler()
	return true
}

// ListScheduledTasks returns all pending tasks matching the given kind.
// If kind is empty, returns all tasks.
func ListScheduledTasks(kind string) []ScheduledTask {
	schedDBMu.Lock()
	db := schedDB
	schedDBMu.Unlock()
	if db == nil {
		return nil
	}
	var out []ScheduledTask
	for _, k := range db.Keys(schedulerTable) {
		var t ScheduledTask
		if !db.Get(schedulerTable, k, &t) {
			continue
		}
		if kind != "" && t.Kind != kind {
			continue
		}
		out = append(out, t)
	}
	return out
}

// UnscheduleTask removes a pending task by ID. The scheduler re-evaluates its sleep target.
func UnscheduleTask(id string) {
	schedDBMu.Lock()
	db := schedDB
	schedDBMu.Unlock()
	if db != nil {
		db.Unset(schedulerTable, id)
	}
	wakeScheduler()
}

// RegisterReconciler registers a periodic reconciliation function for the given kind.
// The scheduler runs all reconcilers every 30 minutes to ensure expected tasks exist.
func RegisterReconciler(kind string, fn func(context.Context) error) {
	reconcilersMu.Lock()
	defer reconcilersMu.Unlock()
	reconcilers[kind] = fn
}

// RunReconcilers executes all registered reconciliation functions, logging errors.
func RunReconcilers(ctx context.Context) {
	reconcilersMu.Lock()
	fns := make(map[string]func(context.Context) error, len(reconcilers))
	for k, v := range reconcilers {
		fns[k] = v
	}
	reconcilersMu.Unlock()

	for name, fn := range fns {
		if err := fn(ctx); err != nil {
			Log("[scheduler] reconciler %q error: %v", name, err)
		}
	}
}

func wakeScheduler() {
	select {
	case schedNotify <- struct{}{}:
	default:
	}
}

// PreInitScheduler sets up the scheduler DB so apps can call ScheduleTask
// during their RegisterRoutes pass (before StartGlobalScheduler starts the loop).
// Call once from ServeDashboard before the RegisterRoutes pass. Idempotent.
func PreInitScheduler() {
	if RootDB == nil {
		return
	}
	schedDBMu.Lock()
	if schedDB == nil {
		schedDB = RootDB.Bucket("scheduler")
	}
	schedDBMu.Unlock()
}

// StartGlobalScheduler launches the background scheduler loop using RootDB.
// Call once from ServeDashboard after all apps have registered their handlers.
// Apps should call SetPostSchedulerStart before this to register reconcilers.
func StartGlobalScheduler(ctx context.Context) {
	schedDBMu.Lock()
	if schedDB == nil {
		if RootDB == nil {
			schedDBMu.Unlock()
			Log("[scheduler] RootDB not set: scheduler disabled")
			return
		}
		schedDB = RootDB.Bucket("scheduler")
	}
	db := schedDB
	schedDBMu.Unlock()

	startupFire(ctx, db)

	go runSchedulerLoop(ctx, db)
	Log("[scheduler] started")

	// Run post-start callbacks (reconciler registration, etc.).
	if postStart != nil {
		postStart()
	}

	// Reconciler ticker: run all reconcilers every 30 minutes to ensure
	// expected tasks actually exist in the scheduler DB.
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		// Run once shortly after startup.
		time.Sleep(5 * time.Second)
		RunReconcilers(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				RunReconcilers(ctx)
			}
		}
	}()

	// Log all pending scheduled tasks.
	schedDBMu.Lock()
	var pending []ScheduledTask
	for _, k := range db.Keys(schedulerTable) {
		var t ScheduledTask
		if db.Get(schedulerTable, k, &t) {
			pending = append(pending, t)
		}
	}
	schedDBMu.Unlock()

	if len(pending) == 0 {
		Log("[scheduler] no pending tasks")
	} else {
		Log("[scheduler] %d pending task(s):", len(pending))
		for _, t := range pending {
			schedHandlersMu.RLock()
			handler := "unknown"
			if schedHandlers[t.Kind] != nil {
				handler = "registered"
			}
			schedHandlersMu.RUnlock()
			if label := DescribeTask(t); label != "" {
				Log("[scheduler]   %s  kind=%s  %s  handler=%s  run_at=%s", t.ID, t.Kind, label, handler, t.RunAt)
			} else {
				Log("[scheduler]   %s  kind=%s  handler=%s  run_at=%s", t.ID, t.Kind, handler, t.RunAt)
			}
		}
	}
}

// runSchedulerLoop sleeps to the next due task, wakes on signal, fires due tasks, repeat.
func runSchedulerLoop(ctx context.Context, db Database) {
	timer := time.NewTimer(nextSleepDuration(db))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-schedNotify:
			// Task added or removed — recalculate next wake-up.
			// Stop the running timer safely before resetting.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			schedDBMu.Lock()
			timer.Reset(nextSleepDuration(db))
			schedDBMu.Unlock()

		case <-timer.C:
			fireDueTasks(ctx, db)
			schedDBMu.Lock()
			timer.Reset(nextSleepDuration(db))
			schedDBMu.Unlock()
		}
	}
}

// nextSleepDuration scans the DB for the earliest pending task and returns the
// duration to sleep. Returns 24h when nothing is pending.
func nextSleepDuration(db Database) time.Duration {
	var earliest time.Time

	for _, k := range db.Keys(schedulerTable) {
		var t ScheduledTask
		if !db.Get(schedulerTable, k, &t) {
			continue
		}
		runAt, err := time.Parse(time.RFC3339, t.RunAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || runAt.Before(earliest) {
			earliest = runAt
		}
	}

	if earliest.IsZero() {
		return 24 * time.Hour
	}
	d := time.Until(earliest)
	if d < 0 {
		return 0
	}
	return d
}

// fireDueTasks executes all tasks with RunAt <= now, removing each before calling
// its handler so a slow or panicking handler can't cause a double-fire.
func fireDueTasks(ctx context.Context, db Database) {
	// Collect tasks to fire while holding the mutex, then fire them outside
	// to avoid data races with UnscheduleTask which modifies the DB.
	schedDBMu.Lock()
	var toFire []ScheduledTask
	queued := queuedTasks(db)
	now := time.Now()
	for _, task := range queued {
		if task.RunAt > now.UTC().Format(time.RFC3339) {
			continue
		}
		db.Unset(schedulerTable, task.ID)
		// A run done again checks once more that its job is not due soon:
		// a chain whose next run was lost with the stop is armed again by
		// its reconciler after the start, before this.
		if task.Resumes > 0 && dueAgainSoon(queued, task, now) {
			Log("[scheduler] task %s (%s) is not run again: the same job runs again within %s", task.ID, taskLabel(task), shortDurationWords(resumeSkipWithin))
			continue
		}
		// A run is on record while it runs, so a stop under it leaves it
		// there for the next start (resumeInterrupted).
		schedHandlersMu.RLock()
		self := schedSelfRecovering[task.Kind]
		schedHandlersMu.RUnlock()
		if !self {
			db.Set(schedulerRunningTable, task.ID, task)
		}
		toFire = append(toFire, task)
	}
	schedDBMu.Unlock()

	for _, task := range toFire {
		schedHandlersMu.RLock()
		fn := schedHandlers[task.Kind]
		schedHandlersMu.RUnlock()

		if fn == nil {
			Log("[scheduler] no handler for kind %q: dropping task %s", task.Kind, task.ID)
			continue
		}
		if label := DescribeTask(task); label != "" {
			Log("[scheduler] firing %s (kind=%s, %s, due=%s)", task.ID, task.Kind, label, task.RunAt)
		} else {
			Log("[scheduler] firing %s (kind=%s, due=%s)", task.ID, task.Kind, task.RunAt)
		}
		// Recover around every handler: the task was already removed from the
		// queue above, and this runs in its own goroutine, so an unrecovered
		// panic here would take down the whole process. recover() contains it
		// to a log line. (Re-arming the recurring chain is the handler's job —
		// each kind that recurs reschedules on its own, e.g. event monitors and
		// orchestrate scheduled updates.)
		handler := fn
		t := task
		go func() {
			defer func() {
				if r := recover(); r != nil {
					Log("[scheduler] handler for kind %q (task %s) panicked, recovered: %v", t.Kind, t.ID, r)
				}
				// Ended: off the record, unless it ended because the process
				// is stopping, which cut it off. A panic is an ending: run
				// again, it would panic again.
				if ctx.Err() == nil {
					schedDBMu.Lock()
					db.Unset(schedulerRunningTable, t.ID)
					schedDBMu.Unlock()
				}
			}()
			handler(ctx, t.Payload)
		}()
	}
}

// startupFire is the scheduler's first pass: queue again the runs a stop cut
// off, then fire every task that came due while the server was offline.
func startupFire(ctx context.Context, db Database) {
	resumeInterrupted(db, time.Now())
	fireDueTasks(ctx, db)
}

// resumeInterrupted queues again, resumeDelay after now, each run a stop cut
// off (left on schedulerRunningTable). It drops one already run again
// maxResumes times, and one whose job is due again within resumeSkipWithin,
// overdue ones included: those fire in this same first pass.
func resumeInterrupted(db Database, now time.Time) {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	queued := queuedTasks(db)
	for _, k := range db.Keys(schedulerRunningTable) {
		var t ScheduledTask
		ok := db.Get(schedulerRunningTable, k, &t)
		db.Unset(schedulerRunningTable, k)
		if !ok {
			continue
		}
		label := taskLabel(t)
		if dueAgainSoon(queued, t, now) {
			Log("[scheduler] task %s (%s) was cut off by the server stopping; not running it again, the same job runs again within %s", t.ID, label, shortDurationWords(resumeSkipWithin))
			continue
		}
		if t.Resumes >= maxResumes {
			Log("[scheduler] task %s (%s) was cut off again after being run again; not running it a third time", t.ID, label)
			continue
		}
		t.Resumes++
		t.RunAt = now.Add(resumeDelay).UTC().Format(time.RFC3339)
		db.Set(schedulerTable, t.ID, t)
		Log("[scheduler] task %s (%s) was cut off by the server stopping; running it again at %s", t.ID, label, t.RunAt)
	}
}

// queuedTasks is every task in the queue. Under schedDBMu.
func queuedTasks(db Database) []ScheduledTask {
	var out []ScheduledTask
	for _, k := range db.Keys(schedulerTable) {
		var t ScheduledTask
		if db.Get(schedulerTable, k, &t) {
			out = append(out, t)
		}
	}
	return out
}

// dueAgainSoon reports whether queued holds another run of t's job (same
// kind, same payload) due before now plus resumeSkipWithin.
func dueAgainSoon(queued []ScheduledTask, t ScheduledTask, now time.Time) bool {
	limit := now.Add(resumeSkipWithin).UTC().Format(time.RFC3339)
	for _, q := range queued {
		if q.ID != t.ID && q.Kind == t.Kind && q.RunAt <= limit && samePayload(q.Payload, t.Payload) {
			return true
		}
	}
	return false
}

// samePayload compares two payloads with their spacing ignored.
func samePayload(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// taskLabel is t's kind and, when its kind has a describer, its label.
func taskLabel(t ScheduledTask) string {
	if d := DescribeTask(t); d != "" {
		return t.Kind + ", " + d
	}
	return t.Kind
}
