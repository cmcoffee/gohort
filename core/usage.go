package core

import (
	"context"
	"sync"
)

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
	// searchClaimed is the share of this scope's search window a NESTED
	// scope already reported on its own line. Tokens and image calls are
	// credited through the context and so land on exactly one tracker;
	// searches are still window-diffed off the process counter (the
	// search tool stack takes no context), which means a parent's window
	// contains its children's searches. Children claim theirs as they
	// finish, the parent subtracts, and the lines sum again.
	searchClaimed int64
	// Cached prompt, kept apart from the uncached remainder because the two
	// are different questions with the same units. SIZE is the sum of all
	// three; COST is a weighted sum, since a cache read bills at a fraction of
	// an ordinary input token and a write at slightly more. Response.InputTokens
	// documents the same split — the tracker simply had not been told.
	workerCacheRead  int64
	workerCacheWrite int64
	leadCacheRead    int64
	leadCacheWrite   int64
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
	// WorkerInput / LeadInput are the UNCACHED remainder only, matching the
	// API's own definition (see Response.InputTokens). The cached share lands
	// here. Use PromptTokens() for the number a human means by "input".
	WorkerCacheRead  int64 `json:"worker_cache_read,omitempty"`
	WorkerCacheWrite int64 `json:"worker_cache_write,omitempty"`
	LeadCacheRead    int64 `json:"lead_cache_read,omitempty"`
	LeadCacheWrite   int64 `json:"lead_cache_write,omitempty"`
}

// WorkerPrompt is the whole worker prompt — what the session UI shows as
// "in", and what a reader means by input tokens.
//
// Reporting WorkerInput alone made a cache-heavy turn look free: a 38,679-token
// prompt served almost entirely from cache reports an uncached remainder of 2,
// so the cost chart said 2 while the session said 38,679. Same bug the v0.5.999
// commit fixed in the UI, unfixed here until the tracker learned the split.
func (s UsageSnapshot) WorkerPrompt() int64 {
	return s.WorkerInput + s.WorkerCacheRead + s.WorkerCacheWrite
}

// LeadPrompt is the whole lead prompt.
func (s UsageSnapshot) LeadPrompt() int64 {
	return s.LeadInput + s.LeadCacheRead + s.LeadCacheWrite
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

// requestUsageKey carries a per-request UsageTracker on a request
// context so instrumentation can credit the request that caused a
// call, not just the process. Installed by UsageReportMiddleware.
type requestUsageKey struct{}

// WithRequestUsage returns a child context carrying a fresh
// UsageTracker plus the tracker itself. The tracker starts at zero,
// so a plain Snapshot() at the end of the scope IS the scope's own
// consumption — no start snapshot to diff against, and no cross-talk
// from concurrent requests or background work that share the process
// tracker. Detached contexts (background pipelines) deliberately drop
// the tracker: their spend belongs to the process, not the request
// that happened to spawn them.
func WithRequestUsage(ctx context.Context) (context.Context, *UsageTracker) {
	t := &UsageTracker{}
	return context.WithValue(ctx, requestUsageKey{}, t), t
}

// CarryRequestUsage copies src's request-scoped tracker onto dst,
// returning dst unchanged when src carries none. For handlers that
// detach the WORK's lifetime from the request but not its ownership:
// a chat turn roots its ctx at Background so a client disconnect can't
// kill an in-flight tool call, yet every token that turn spends is
// still this request's spend. Without this, the request tracker never
// moves and the end-of-request cost line — which skips on a zero delta
// — never prints at all for exactly the requests worth costing.
//
// Distinct from the deliberate drop described on WithRequestUsage:
// that's for work the request merely SPAWNS and doesn't wait on
// (background ingest, scheduled follow-ups). Use this only when the
// handler blocks until the detached work is done.
func CarryRequestUsage(dst, src context.Context) context.Context {
	if t := RequestUsage(src); t != nil {
		return context.WithValue(dst, requestUsageKey{}, t)
	}
	return dst
}

// WithSubUsage scopes a nested run's spend onto its OWN tracker and
// returns a defer-friendly closure that logs it on its own cost line.
// The fresh tracker shadows any parent tracker on ctx, so every token
// the nested run spends is credited to it and NOT to the request that
// spawned it — a dispatched sub-agent gets its own report rather than
// silently inflating its delegator's.
//
// Searches are the exception the whole tracker shares: they are
// window-diffed off the process counter, so the scope's own window
// still contains any searches its children ran. The closure claims
// this scope's count against the parent tracker (ClaimSearchCalls) and
// reports its own count net of what ITS children claimed, so a nested
// chain of lines still sums to the process total.
//
//	ctx, reportUsage := WithSubUsage(ctx, "dispatch "+name+" "+runID)
//	defer reportUsage()
//
// Skips the log when nothing moved, matching every other report path.
func WithSubUsage(ctx context.Context, label string) (context.Context, func()) {
	parent := RequestUsage(ctx)
	globalStart := ProcessUsage().Snapshot()
	sub, t := WithRequestUsage(ctx)
	return sub, func() {
		d := t.Snapshot()
		window := ProcessUsage().Diff(globalStart).SearchCalls
		d.SearchCalls = t.UnclaimedSearchCalls(window)
		if parent != nil {
			parent.ClaimSearchCalls(window)
		}
		if d == (UsageDiff{}) {
			return
		}
		Log("%s", FormatUsageReport(label, d))
	}
}

// RequestUsage returns the context's request-scoped tracker, or nil
// when the context isn't wrapped (background jobs, scheduled tasks,
// detached pipeline contexts). Instrumentation points nil-check and
// skip — the process-wide tracker still counts everything.
func RequestUsage(ctx context.Context) *UsageTracker {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(requestUsageKey{}).(*UsageTracker)
	return t
}

// AddWorker records token consumption from a worker-tier LLM call.
// Safe to call with zero values (no-op when provider didn't report
// token counts). Also rolls the diff into the persistent daily-cost
// log so the admin's Cost History chart reflects every call.
func (u *UsageTracker) AddWorker(input, output int) {
	u.AddWorkerTokens(input, output, 0, 0)
}

// AddWorkerTokens records a worker-tier call including its cached prompt.
func (u *UsageTracker) AddWorkerTokens(input, output, cacheRead, cacheWrite int) {
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		return
	}
	u.mu.Lock()
	u.workerInput += int64(input)
	u.workerOutput += int64(output)
	u.workerCacheRead += int64(cacheRead)
	u.workerCacheWrite += int64(cacheWrite)
	u.mu.Unlock()
	// Persist only from the process singleton. Request-scoped trackers
	// (WithRequestUsage) see the same Add calls as the global; without
	// this guard every call reached the daily rollup twice and the admin
	// chart doubled.
	if u == processUsage {
		recordDailyUsage(UsageDiff{
			WorkerInput: int64(input), WorkerOutput: int64(output),
			WorkerCacheRead: int64(cacheRead), WorkerCacheWrite: int64(cacheWrite),
		})
	}
}

// AddLead records token consumption from a lead-tier LLM call.
func (u *UsageTracker) AddLead(input, output int) {
	u.AddLeadTokens(input, output, 0, 0)
}

// AddLeadTokens records a lead-tier call including its cached prompt.
func (u *UsageTracker) AddLeadTokens(input, output, cacheRead, cacheWrite int) {
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		return
	}
	u.mu.Lock()
	u.leadInput += int64(input)
	u.leadOutput += int64(output)
	u.leadCacheRead += int64(cacheRead)
	u.leadCacheWrite += int64(cacheWrite)
	u.mu.Unlock()
	if u == processUsage {
		recordDailyUsage(UsageDiff{
			LeadInput: int64(input), LeadOutput: int64(output),
			LeadCacheRead: int64(cacheRead), LeadCacheWrite: int64(cacheWrite),
		})
	}
}

// AddSearchCall increments the external search counter. Should fire
// only for real provider hits — cache hits should NOT bump it, since
// they don't consume search-API quota.
func (u *UsageTracker) AddSearchCall() {
	u.mu.Lock()
	u.searchCalls++
	u.mu.Unlock()
	if u == processUsage {
		recordDailyUsage(UsageDiff{SearchCalls: 1})
	}
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
	if u == processUsage {
		recordDailyUsage(UsageDiff{ImageCalls: 1})
	}
}

// ClaimSearchCalls records that n searches inside this scope's window
// were already reported by a nested scope on its own line. See
// searchClaimed and WithSubUsage.
func (u *UsageTracker) ClaimSearchCalls(n int64) {
	if n <= 0 {
		return
	}
	u.mu.Lock()
	u.searchClaimed += n
	u.mu.Unlock()
}

// UnclaimedSearchCalls returns the share of a window-diffed search
// count that belongs to THIS scope — the window minus whatever nested
// scopes claimed. Never negative: a child that outlives its parent's
// report can claim more than the parent saw, and a negative search
// count in a cost line is worse than a low one.
func (u *UsageTracker) UnclaimedSearchCalls(window int64) int64 {
	u.mu.Lock()
	claimed := u.searchClaimed
	u.mu.Unlock()
	if n := window - claimed; n > 0 {
		return n
	}
	return 0
}

// Snapshot returns a consistent read of the current counter values.
func (u *UsageTracker) Snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return UsageSnapshot{
		WorkerInput:      u.workerInput,
		WorkerOutput:     u.workerOutput,
		LeadInput:        u.leadInput,
		LeadOutput:       u.leadOutput,
		SearchCalls:      u.searchCalls,
		ImageCalls:       u.imageCalls,
		WorkerCacheRead:  u.workerCacheRead,
		WorkerCacheWrite: u.workerCacheWrite,
		LeadCacheRead:    u.leadCacheRead,
		LeadCacheWrite:   u.leadCacheWrite,
	}
}

// Diff returns the delta between the given start snapshot and the
// current counter values. Used at per-run boundaries: snap at start,
// diff at end.
func (u *UsageTracker) Diff(start UsageSnapshot) UsageDiff {
	now := u.Snapshot()
	return UsageDiff{
		WorkerInput:      now.WorkerInput - start.WorkerInput,
		WorkerOutput:     now.WorkerOutput - start.WorkerOutput,
		LeadInput:        now.LeadInput - start.LeadInput,
		LeadOutput:       now.LeadOutput - start.LeadOutput,
		SearchCalls:      now.SearchCalls - start.SearchCalls,
		ImageCalls:       now.ImageCalls - start.ImageCalls,
		WorkerCacheRead:  now.WorkerCacheRead - start.WorkerCacheRead,
		WorkerCacheWrite: now.WorkerCacheWrite - start.WorkerCacheWrite,
		LeadCacheRead:    now.LeadCacheRead - start.LeadCacheRead,
		LeadCacheWrite:   now.LeadCacheWrite - start.LeadCacheWrite,
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
