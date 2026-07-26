// Run + RunRegistry — the in-memory ledger that lets an agent turn
// survive the HTTP request that started it. Created when handleSend
// fires; the agent loop writes SSE frames into the Run's event ring,
// and HTTP clients (the chat panel's live SSE OR a fresh /api/runs/
// /<id>/stream subscriber after a disconnect) tail those frames.
//
// Lifecycle:
//
//   Running  → Completed | Failed | Canceled
//
// Status transitions are one-way; Complete is idempotent (re-calls
// after the first are no-ops). Subscribers receive every event from
// their `since` cursor through completion; the live channel closes
// when the run finishes so tailers know to stop reading.
//
// In-memory only. A server restart drops every active run; the
// session row in kvlite still has the pre-restart messages, so the
// user just sees "the agent didn't finish that turn." Persistence is
// deferred — see [[project_async_dispatch]] for the planned arc.
//
// Concurrency: each Run carries its own mutex. The registry's mutex
// is separate and only held during create / lookup / cleanup —
// never during Run operations, so a hot run never blocks the
// registry.

package orchestrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Run status values. Strings so they serialize cleanly for any
// future status endpoint (admin dashboard, etc.).
const (
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCanceled  = "canceled"
)

// runOutcomeStatus maps a loop's exit error to a run status. A context
// cancellation (the turn was SUPERSEDED by a newer send, or the process is
// shutting down) is CANCELED, not FAILED — otherwise a dispatched sub-agent
// whose parent turn was replaced gets stamped "failed" when it did nothing
// wrong. A genuine error, or a turn that produced no result at all, is FAILED;
// everything else (incl. hitting the round cap, which returns a nil error) is
// COMPLETED.
func runOutcomeStatus(runErr error, hasResult bool) string {
	switch {
	case runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)):
		return RunStatusCanceled
	case runErr != nil || !hasResult:
		return RunStatusFailed
	default:
		return RunStatusCompleted
	}
}

func init() {
	RegisterTunable(TunableSpec{Key: "tune_run_max_events", Category: "Limits", Label: "Run event ring size", Help: "Max in-memory SSE events buffered per run.", Kind: KindInt, Default: 500, Min: 100, Max: 5000})
	RegisterTunable(TunableSpec{Key: "tune_run_cleanup_age", Category: "Cache", Label: "Run buffer retention", Help: "How long a completed run's buffer is kept for reconnect.", Kind: KindMinutes, Default: 30, Min: 5, Max: 240})
	RegisterTunable(TunableSpec{Key: "tune_run_cleanup_interval", Category: "Timeouts", Label: "Run sweep interval", Help: "How often the registry sweeps for old completed runs.", Kind: KindMinutes, Default: 5, Min: 1, Max: 60})
}

// runMaxEvents bounds the in-memory ring per run. Chat turns are
// short — typical worker-loop emits 20-50 events. 500 leaves plenty
// of headroom for long planning runs while keeping memory bounded.
func runMaxEvents() int { return TuneInt("tune_run_max_events") }

// runCleanupAge — how long to keep a completed Run's buffer around
// after it finishes. Long enough for a desktop user to reconnect
// after a sleep / network blip; short enough that memory stays sane.
func runCleanupAge() time.Duration { return TuneDuration("tune_run_cleanup_age") }

// runCleanupInterval — how often the registry sweeps for old runs.
func runCleanupInterval() time.Duration { return TuneDuration("tune_run_cleanup_interval") }

// RunEvent is one pre-serialized SSE frame plus its sequence number.
// Stored ready-to-write so subscribers can replay backlog with no
// re-marshaling. Frame is the literal bytes that would have gone to
// the live response — `data: …\n\n` or `event: …\ndata: …\n\n`.
type RunEvent struct {
	Seq   uint64
	Frame []byte
}

// Subscription is what Subscribe returns. Backlog holds every event
// with Seq > since at subscription time; Live delivers later events
// as they arrive. Live closes when the run completes — subscribers
// drain it until close to know they've seen the final frame.
type Subscription struct {
	id      uint64
	Backlog []RunEvent
	Live    <-chan RunEvent
}

// Run is one in-flight (or recently-completed) agent turn.
type Run struct {
	ID        string
	UserID    string
	AgentID   string
	SessionID string

	mu          sync.Mutex
	status      string
	startedAt   time.Time
	endedAt     time.Time
	events      []RunEvent
	nextSeq     uint64
	subscribers map[uint64]chan RunEvent
	nextSubID   uint64
	cancel      context.CancelFunc
	closed      bool

	// Activity metadata — what the live "Active now" surface renders. kind
	// says which surface started the turn (chat | scheduled | standing |
	// dispatch); agentName/label are display strings resolved at create
	// time so the surface never has to join against the agent store.
	// round/lastTool are best-effort progress fed from the loop's OnStep.
	kind      string
	agentName string
	label     string
	round     int
	lastTool  string
	parentID  string // the run that spawned this one (a delegating turn); "" for a top-level turn
}

// runCtxKey carries the enclosing run's ID down the context so a nested
// dispatch can record its parent — the chain the live surface renders as a
// tree (turn → sub-agents it called → what they're doing).
type runCtxKeyT struct{}

var runCtxKey runCtxKeyT

// withParentRun tags ctx with the ID of the run currently executing under it.
// Every run-creation site calls this on the ctx it hands to the loop, so any
// dispatch that fires inside inherits the ID as its parent.
func withParentRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runCtxKey, runID)
}

// parentRunFromCtx returns the enclosing run ID tagged on ctx, or "" at the top.
func parentRunFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(runCtxKey).(string); ok {
		return id
	}
	return ""
}

// Parent records which run spawned this one (chainable, set-once at creation).
func (r *Run) Parent(parentID string) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentID = parentID
	return r
}

// Describe stamps the activity metadata onto a run (chainable, set-once at
// creation). Separate from Create so the signature stays stable.
func (r *Run) Describe(kind, agentName, label string) *Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kind, r.agentName, r.label = kind, agentName, label
	return r
}

// SetProgress records best-effort live progress (current round + the tools
// the model just fired) for the activity surface. Safe from OnStep hooks.
func (r *Run) SetProgress(round int, tools []ToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if round > r.round {
		r.round = round
	}
	if n := len(tools); n > 0 {
		r.lastTool = tools[n-1].Name
	}
}

// RunSnapshot is a lock-free copy of a run's activity view.
type RunSnapshot struct {
	ID        string
	UserID    string
	AgentID   string
	SessionID string
	Kind      string
	AgentName string
	Label     string
	Status    string
	Round     int
	LastTool  string
	StartedAt time.Time
	EndedAt   time.Time
	ParentID  string // "" for a top-level turn
	Depth     int    // levels below a top-level turn (set by tree ordering, not stored)
}

// Snapshot returns the activity view of this run.
func (r *Run) Snapshot() RunSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunSnapshot{
		ID: r.ID, UserID: r.UserID, AgentID: r.AgentID, SessionID: r.SessionID,
		Kind: r.kind, AgentName: r.agentName, Label: r.label,
		Status: r.status, Round: r.round, LastTool: r.lastTool,
		StartedAt: r.startedAt, EndedAt: r.endedAt, ParentID: r.parentID,
	}
}

// Status returns the current run status (snapshot — caller doesn't
// hold the lock past return).
func (r *Run) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// StartedAt / EndedAt — for the future status surface. EndedAt is
// the zero value while still running.
func (r *Run) StartedAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startedAt
}
func (r *Run) EndedAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endedAt
}

// Append records one SSE frame from the agent loop. Called by the
// sseWriter's run-mode io.Writer. Sequence numbers start at 1 and
// monotonically increase; the ring drops the oldest when it grows
// past runMaxEvents (subscribers that fall too far behind get a
// "missed events" gap — acceptable for chat where turns are short).
//
// No-op after Complete. The agent loop's wrapped writer can keep
// calling Append even after a cancel races; we just absorb.
func (r *Run) Append(frame []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.nextSeq++
	ev := RunEvent{Seq: r.nextSeq, Frame: append([]byte(nil), frame...)}
	r.events = append(r.events, ev)
	if max := runMaxEvents(); len(r.events) > max {
		// Drop oldest. Cheap append+slice; ring grows once then
		// stays at runMaxEvents for the rest of the run.
		r.events = r.events[len(r.events)-max:]
	}
	for _, ch := range r.subscribers {
		// Non-blocking — a slow subscriber loses events rather than
		// jamming the loop. Buffer is 64 (set in Subscribe); only a
		// stuck client would overflow that.
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe registers a new live subscriber. Backlog contains every
// event with Seq > since at the moment of subscription; Live
// delivers subsequent events. Cleanup MUST call Unsubscribe (e.g.
// via defer) or the channel leaks.
//
// When the run has already completed, Subscribe still returns the
// full backlog past `since` — the Live channel is returned
// pre-closed so callers can drain it once and exit.
func (r *Run) Subscribe(since uint64) Subscription {
	r.mu.Lock()
	defer r.mu.Unlock()
	var backlog []RunEvent
	for _, ev := range r.events {
		if ev.Seq > since {
			backlog = append(backlog, ev)
		}
	}
	if r.closed {
		// Already done — backlog is everything; no live channel needed.
		closed := make(chan RunEvent)
		close(closed)
		return Subscription{Backlog: backlog, Live: closed}
	}
	if r.subscribers == nil {
		r.subscribers = make(map[uint64]chan RunEvent)
	}
	r.nextSubID++
	id := r.nextSubID
	ch := make(chan RunEvent, 64)
	r.subscribers[id] = ch
	return Subscription{id: id, Backlog: backlog, Live: ch}
}

// Unsubscribe drops a subscriber. Idempotent.
func (r *Run) Unsubscribe(s Subscription) {
	if s.id == 0 {
		return // Subscribe to an already-closed run uses id=0 / pre-closed chan.
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.subscribers[s.id]; ok {
		close(ch)
		delete(r.subscribers, s.id)
	}
}

// Cancel triggers the agent loop's cancel context. The loop will
// shortly see ctx.Done() and emit a final cancellation-related
// event, then Complete will fire. Idempotent.
func (r *Run) Cancel() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Complete marks the run finished and closes every subscriber.
// Idempotent — handleSend's defer chain may call this multiple times
// (panic recovery + normal exit); only the first call sticks.
func (r *Run) Complete(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.status = status
	r.endedAt = time.Now()
	for _, ch := range r.subscribers {
		close(ch)
	}
	r.subscribers = nil
}

// RunRegistry tracks live + recently-completed runs. One per
// OrchestrateApp instance. A background sweeper drops completed
// runs older than runCleanupAge so the map doesn't grow unbounded.
type RunRegistry struct {
	mu      sync.Mutex
	runs    map[string]*Run // by run ID
	bySess  map[string]*Run // by session ID — at most one active run per session
	sweeper sync.Once
}

// NewRunRegistry constructs an empty registry. The first Create
// call starts the cleanup sweeper goroutine.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{
		runs:   make(map[string]*Run),
		bySess: make(map[string]*Run),
	}
}

// Create starts a new Run, registers it under both its ID and its
// session ID. If a prior run for the same session is still active,
// it's canceled first — same behavior the request-bound model had
// via inflightCancels.
func (rr *RunRegistry) Create(userID, agentID, sessionID string, cancel context.CancelFunc) *Run {
	rr.startSweeper()

	rr.mu.Lock()
	defer rr.mu.Unlock()

	// Replace any active run on this session — preserves the
	// "fresh send cancels old" semantics from inflightCancels.
	if prev, ok := rr.bySess[sessionID]; ok {
		go prev.Cancel()
		delete(rr.bySess, sessionID)
	}

	r := &Run{
		ID:        generateRunID(),
		UserID:    userID,
		AgentID:   agentID,
		SessionID: sessionID,
		status:    RunStatusRunning,
		startedAt: time.Now(),
		cancel:    cancel,
	}
	rr.runs[r.ID] = r
	if sessionID != "" {
		rr.bySess[sessionID] = r
	}
	return r
}

// Get returns the run by ID, or nil if it's been swept.
func (rr *RunRegistry) Get(id string) *Run {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.runs[id]
}

// BySession returns the active run for a session, or nil if there
// is none right now. Used by the chat panel to discover whether to
// resume a stream after reconnect.
func (rr *RunRegistry) BySession(sessionID string) *Run {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	r := rr.bySess[sessionID]
	if r == nil {
		return nil
	}
	// Only return as "active" if still running — once completed the
	// session no longer has an inflight run, even though we may
	// still keep the buffer for a few minutes.
	if r.Status() != RunStatusRunning {
		return nil
	}
	return r
}

// Activity returns the user's runs for the live "Active now" surface:
// every running run first (oldest first — longest-running on top reads as
// "what's been going on"), then recently-completed ones newest first. The
// sweeper's retention (runCleanupAge) is the natural history window.
func (rr *RunRegistry) Activity(userID string) []RunSnapshot {
	rr.mu.Lock()
	all := make([]*Run, 0, len(rr.runs))
	for _, r := range rr.runs {
		all = append(all, r)
	}
	rr.mu.Unlock() // snapshot below takes each run's own lock

	var active, done []RunSnapshot
	for _, r := range all {
		s := r.Snapshot()
		if s.UserID != userID {
			continue
		}
		if s.Status == RunStatusRunning {
			active = append(active, s)
		} else {
			done = append(done, s)
		}
	}
	active = orderRunsByTree(active) // parent→child DFS with Depth for the nested view
	sort.Slice(done, func(i, j int) bool { return done[i].EndedAt.After(done[j].EndedAt) })
	return append(active, done...)
}

// ActiveSnapshots returns a snapshot of every currently-running run across all
// users — for the global live ribbon (the pill that shows running apps), which
// has no per-user scope, matching the app-side LiveProviders and the untenanted
// /api/live handler. Done and not-yet-started runs are excluded.
func (rr *RunRegistry) ActiveSnapshots() []RunSnapshot {
	rr.mu.Lock()
	all := make([]*Run, 0, len(rr.runs))
	for _, r := range rr.runs {
		all = append(all, r)
	}
	rr.mu.Unlock() // snapshot below takes each run's own lock

	var out []RunSnapshot
	for _, r := range all {
		if s := r.Snapshot(); s.Status == RunStatusRunning {
			out = append(out, s)
		}
	}
	return orderRunsByTree(out)
}

// orderRunsByTree returns snaps in parent→child DFS order with Depth set on
// each, so the live surface can render nesting: a delegating turn, then the
// sub-agents it spawned indented beneath it, recursively. A run whose parent
// isn't in the set (parent already finished, or a top-level turn) is a root at
// depth 0. Cycle-safe via a visited set; ties break by start time.
func orderRunsByTree(snaps []RunSnapshot) []RunSnapshot {
	present := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		present[s.ID] = true
	}
	children := make(map[string][]RunSnapshot)
	var roots []RunSnapshot
	for _, s := range snaps {
		if s.ParentID != "" && present[s.ParentID] {
			children[s.ParentID] = append(children[s.ParentID], s)
		} else {
			roots = append(roots, s)
		}
	}
	byStart := func(list []RunSnapshot) {
		sort.Slice(list, func(i, j int) bool { return list[i].StartedAt.Before(list[j].StartedAt) })
	}
	byStart(roots)
	for k := range children {
		byStart(children[k])
	}
	out := make([]RunSnapshot, 0, len(snaps))
	visited := make(map[string]bool, len(snaps))
	var walk func(s RunSnapshot, depth int)
	walk = func(s RunSnapshot, depth int) {
		if visited[s.ID] {
			return
		}
		visited[s.ID] = true
		s.Depth = depth
		out = append(out, s)
		for _, c := range children[s.ID] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	for _, s := range snaps { // any cycle stragglers — append flat
		if !visited[s.ID] {
			s.Depth = 0
			out = append(out, s)
		}
	}
	return out
}

// startSweeper lazily starts the background cleanup loop. Each
// tick scans for completed runs past runCleanupAge and drops them
// from both maps. sync.Once guarantees one sweeper per registry.
func (rr *RunRegistry) startSweeper() {
	rr.sweeper.Do(func() {
		go func() {
			ticker := time.NewTicker(runCleanupInterval())
			defer ticker.Stop()
			for range ticker.C {
				rr.sweep()
			}
		}()
	})
}

func (rr *RunRegistry) sweep() {
	cutoff := time.Now().Add(-runCleanupAge())
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for id, r := range rr.runs {
		r.mu.Lock()
		expired := r.closed && r.endedAt.Before(cutoff)
		sess := r.SessionID
		r.mu.Unlock()
		if expired {
			delete(rr.runs, id)
			if sess != "" && rr.bySess[sess] == r {
				delete(rr.bySess, sess)
			}
		}
	}
}

// SetCancel attaches the loop's cancel function to the run AFTER
// creation. Create gets called before context.WithCancel in
// handleSend (we need the run ID before deriving the ctx so the run
// is registered atomically), so the cancel func is set in a second
// step.
func (r *Run) SetCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = cancel
}

// generateRunID produces an opaque 24-hex-char ID. Crypto-random so
// the ID can be safely exposed in URLs without enumeration risk.
func generateRunID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback — wall clock + monotonic, still good-enough
		// uniqueness for an in-memory ledger.
		Err("[orchestrate.runs] crypto/rand failed: %v", err)
		t := time.Now().UnixNano()
		for i := 0; i < 12; i++ {
			b[i] = byte(t >> (8 * (i % 8)))
		}
	}
	return hex.EncodeToString(b[:])
}
