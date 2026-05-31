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

// runMaxEvents bounds the in-memory ring per run. Chat turns are
// short — typical worker-loop emits 20-50 events. 500 leaves plenty
// of headroom for long planning runs while keeping memory bounded.
const runMaxEvents = 500

// runCleanupAge — how long to keep a completed Run's buffer around
// after it finishes. Long enough for a desktop user to reconnect
// after a sleep / network blip; short enough that memory stays sane.
const runCleanupAge = 30 * time.Minute

// runCleanupInterval — how often the registry sweeps for old runs.
const runCleanupInterval = 5 * time.Minute

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
	if len(r.events) > runMaxEvents {
		// Drop oldest. Cheap append+slice; ring grows once then
		// stays at runMaxEvents for the rest of the run.
		r.events = r.events[len(r.events)-runMaxEvents:]
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

// startSweeper lazily starts the background cleanup loop. Each
// tick scans for completed runs past runCleanupAge and drops them
// from both maps. sync.Once guarantees one sweeper per registry.
func (rr *RunRegistry) startSweeper() {
	rr.sweeper.Do(func() {
		go func() {
			ticker := time.NewTicker(runCleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				rr.sweep()
			}
		}()
	})
}

func (rr *RunRegistry) sweep() {
	cutoff := time.Now().Add(-runCleanupAge)
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
