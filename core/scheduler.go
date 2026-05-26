package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const schedulerTable = "scheduler_tasks"

// ScheduledTask is a persisted deferred job stored in the global scheduler DB.
type ScheduledTask struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`    // matches a RegisterScheduleHandler key
	Payload json.RawMessage `json:"payload"` // app-defined; passed verbatim to the handler
	RunAt   string          `json:"run_at"`  // RFC3339 UTC
	Created string          `json:"created"`
}

// ScheduleHandlerFunc is called when a task becomes due.
// payload is the raw JSON that was passed to ScheduleTask.
type ScheduleHandlerFunc func(ctx context.Context, payload json.RawMessage)

var (
	schedHandlers   = map[string]ScheduleHandlerFunc{}
	schedHandlersMu sync.RWMutex

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
	reconcilers = map[string]func(context.Context) error{}
	reconcilersMu sync.Mutex
)

// RegisterScheduleHandler registers a handler for the given task kind.
// Call from an app's RegisterRoutes (or init) before StartGlobalScheduler is called.
func RegisterScheduleHandler(kind string, fn ScheduleHandlerFunc) {
	schedHandlersMu.Lock()
	schedHandlers[kind] = fn
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

// SetPostSchedulerStart sets a callback invoked once after the scheduler loop starts.
// Apps use this to register reconcilers and perform one-time setup.
func SetPostSchedulerStart(fn func()) {
	postStartMu.Lock()
	defer postStartMu.Unlock()
	postStart = fn
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

// GetSchedDB returns the scheduler DB (nil if not initialized).
func GetSchedDB() Database {
	schedDBMu.Lock()
	defer schedDBMu.Unlock()
	return schedDB
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
			Log("[scheduler] RootDB not set — scheduler disabled")
			return
		}
		schedDB = RootDB.Bucket("scheduler")
	}
	db := schedDB
	schedDBMu.Unlock()

	// Fire any tasks that were due while the server was offline.
	fireDueTasks(ctx, db)

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
			Log("[scheduler]   %s  kind=%s  handler=%s  run_at=%s", t.ID, t.Kind, handler, t.RunAt)
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
	for _, k := range db.Keys(schedulerTable) {
		var task ScheduledTask
		if !db.Get(schedulerTable, k, &task) {
			continue
		}
		if task.RunAt > time.Now().UTC().Format(time.RFC3339) {
			continue
		}
		db.Unset(schedulerTable, k)
		toFire = append(toFire, task)
	}
	schedDBMu.Unlock()

	for _, task := range toFire {
		schedHandlersMu.RLock()
		fn := schedHandlers[task.Kind]
		schedHandlersMu.RUnlock()

		if fn == nil {
			Log("[scheduler] no handler for kind %q — dropping task %s", task.Kind, task.ID)
			continue
		}
		Log("[scheduler] firing %s (kind=%s, due=%s)", task.ID, task.Kind, task.RunAt)
		go fn(ctx, task.Payload)
	}
}
