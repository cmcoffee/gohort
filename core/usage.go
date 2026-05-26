package core

import "sync"

// UsageTracker accumulates LLM token counts (split by worker / lead
// tier) and search API calls across a process. Instrumentation lives
// inside WorkerChat, LeadChat, and CachedCrossSearch, so any app
// using those paths gets its usage counted automatically — no code
// in the app itself. For per-run attribution, use Snapshot() + Diff()
// or the Scope() helper.
//
// Rationale for process-global state: pipeline code scatters LLM
// calls across many packages that don't share a context handle.
// Threading a tracker handle through every call would bloat
// signatures everywhere; the singleton lets instrumentation happen
// silently.
// Tests that need isolation use the per-snapshot Diff pattern —
// the global never resets, but a test captures Snapshot-at-start
// and Diff-at-end to see exactly what it consumed.
type UsageTracker struct {
	mu           sync.Mutex
	workerInput  int64
	workerOutput int64
	leadInput    int64
	leadOutput   int64
	searchCalls  int64
	imageCalls   int64
}

// UsageSnapshot is an immutable copy of the tracker's counters at
// the moment of the snapshot. Produced by Snapshot(); consumed by
// Diff() to compute deltas between two points in time.
type UsageSnapshot struct {
	WorkerInput  int64
	WorkerOutput int64
	LeadInput    int64
	LeadOutput   int64
	SearchCalls  int64
	ImageCalls   int64
}

// UsageDiff is the per-run delta — identical shape to UsageSnapshot,
// aliased for readability at call sites (Diff returns usage consumed
// BETWEEN two snapshots, not absolute counts since process start).
type UsageDiff = UsageSnapshot

// processUsage is the global tracker. Apps don't reference it
// directly; they call Snapshot() / Diff() / Scope() which all work
// against this singleton.
var processUsage = &UsageTracker{}

// ProcessUsage returns the shared tracker. Primarily useful for
// telemetry readers — instrumentation points should use the
// AddWorker / AddLead / AddSearchCall helpers on the singleton.
func ProcessUsage() *UsageTracker { return processUsage }

// AddWorker records token consumption from a worker-tier LLM call.
// Safe to call with zero values (no-op when provider didn't report
// token counts). Also rolls the diff into the persistent daily-cost
// log so the admin's Cost History chart reflects every call.
func (u *UsageTracker) AddWorker(input, output int) {
	if input == 0 && output == 0 {
		return
	}
	u.mu.Lock()
	u.workerInput += int64(input)
	u.workerOutput += int64(output)
	u.mu.Unlock()
	recordDailyUsage(UsageDiff{WorkerInput: int64(input), WorkerOutput: int64(output)})
}

// AddLead records token consumption from a lead-tier LLM call.
func (u *UsageTracker) AddLead(input, output int) {
	if input == 0 && output == 0 {
		return
	}
	u.mu.Lock()
	u.leadInput += int64(input)
	u.leadOutput += int64(output)
	u.mu.Unlock()
	recordDailyUsage(UsageDiff{LeadInput: int64(input), LeadOutput: int64(output)})
}

// AddSearchCall increments the external search counter. Should fire
// only for real provider hits — cache hits should NOT bump it, since
// they don't consume search-API quota.
func (u *UsageTracker) AddSearchCall() {
	u.mu.Lock()
	u.searchCalls++
	u.mu.Unlock()
	recordDailyUsage(UsageDiff{SearchCalls: 1})
}

// AddImageCall increments the image-generation counter. Fires per
// successful GenerateImage call regardless of provider (DALL-E,
// Imagen, etc.). Priced per-call in CostRates — not per-resolution;
// if provider pricing ever diverges sharply by resolution we'd add a
// tier dimension, but for now the flat per-image charge approximates
// well enough (typical header-image usage is a single resolution).
func (u *UsageTracker) AddImageCall() {
	u.mu.Lock()
	u.imageCalls++
	u.mu.Unlock()
	recordDailyUsage(UsageDiff{ImageCalls: 1})
}

// Snapshot returns a consistent read of the current counter values.
func (u *UsageTracker) Snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return UsageSnapshot{
		WorkerInput:  u.workerInput,
		WorkerOutput: u.workerOutput,
		LeadInput:    u.leadInput,
		LeadOutput:   u.leadOutput,
		SearchCalls:  u.searchCalls,
		ImageCalls:   u.imageCalls,
	}
}

// Diff returns the delta between the given start snapshot and the
// current counter values. Used at per-run boundaries: snap at start,
// diff at end.
func (u *UsageTracker) Diff(start UsageSnapshot) UsageDiff {
	now := u.Snapshot()
	return UsageDiff{
		WorkerInput:  now.WorkerInput - start.WorkerInput,
		WorkerOutput: now.WorkerOutput - start.WorkerOutput,
		LeadInput:    now.LeadInput - start.LeadInput,
		LeadOutput:   now.LeadOutput - start.LeadOutput,
		SearchCalls:  now.SearchCalls - start.SearchCalls,
		ImageCalls:   now.ImageCalls - start.ImageCalls,
	}
}

// Scope is the ergonomic form for per-run attribution. Capture the
// start snapshot on call; invoke the returned function at defer time
// to compute the delta and pass it to onDone. Pattern:
//
//	defer core.Scope(func(d core.UsageDiff) {
//	    // persist d onto the run record, emit an event, log, etc.
//	})()
//
// One line at the top of any per-run function; no other code in
// the run body needs to know about usage tracking.
func Scope(onDone func(UsageDiff)) func() {
	start := processUsage.Snapshot()
	return func() {
		if onDone != nil {
			onDone(processUsage.Diff(start))
		}
	}
}
