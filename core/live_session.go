package core

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// --- Generic live session management for SSE-based agents ---

// LiveSession tracks a single running agent session so it can be cancelled
// independently of the HTTP connection. Events are buffered so reconnecting
// clients can catch up.
type LiveSession[T any] struct {
	ID      string
	Label   string // Human-readable label (topic, question, etc.)
	Owner   string // User this session belongs to; surfaces as LiveEntry.Owner so /api/live can mask the label for everyone else. Set via SetOwner — empty masks for all.
	Cancel  context.CancelFunc
	Events  []T
	Done    bool
	Status  string // Last status message for live view
	Spawned bool   // True if spawned by a parent session. Cannot be cancelled directly -- cancel the parent instead.
	// Restoring is true for a session that was rehydrated from the
	// persistent queue on startup but whose work goroutine has not yet
	// begun. A race between restore and a stale browser cancel (e.g. a
	// reload-after-restart firing its auto-cancel) will otherwise kill
	// the pipeline the instant it resumes. HandleCancel rejects cancels
	// in this window with 409 Conflict; set the flag in OnRegister on
	// the restore path and clear it from PipelineConfig.OnStarted.
	Restoring bool
}

// LiveSessionMap manages concurrent live sessions with mutex protection,
// concurrency limits, and 10-minute cleanup after completion.
type LiveSessionMap[T any] struct {
	mu       sync.Mutex
	sessions map[string]*LiveSession[T]
}

// NewLiveSessionMap creates a new session map.
func NewLiveSessionMap[T any](maxConcurrent int) *LiveSessionMap[T] {
	return &LiveSessionMap[T]{
		sessions: make(map[string]*LiveSession[T]),
	}
}

// Register adds a new session to the map.
func (m *LiveSessionMap[T]) Register(id, label string, cancel context.CancelFunc) *LiveSession[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &LiveSession[T]{ID: id, Label: label, Cancel: cancel}
	m.sessions[id] = s
	return s
}

// SetOwner records which user this session belongs to and returns the session,
// so a caller can tag it inline: Register(id, q, cancel).SetOwner(user).
//
// Labels here are user content — the research question, the debate topic, the
// command being mapped. Without an owner the global live ribbon masks the
// label for everyone, so tagging a session is what lets its OWN user keep
// seeing what it is. Nil-safe: Register returns nil at the concurrency cap.
func (s *LiveSession[T]) SetOwner(user string) *LiveSession[T] {
	if s == nil {
		return nil
	}
	s.Owner = user
	return s
}

// OwnerOf returns the user a session belongs to and whether the session exists
// at all. Reads under the map's lock, so it is the safe way to ask; reaching
// through Get and touching s.Owner races with SetOwner.
func (m *LiveSessionMap[T]) OwnerOf(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return "", false
	}
	return s.Owner, true
}

// MayView reports whether viewer may read or stop the work in session id.
//
// A session id is not a capability. It appears in every /api/live payload,
// which is global and untenanted on purpose, so the id of somebody else's run
// is ordinary public knowledge — the events behind it are not. This is the
// check that keeps those two facts apart, and every handler that takes an id
// off a query string has to run it.
//
// Three rules, in order:
//
//   - Auth not configured (no users) means a single-tenant deployment where
//     there is no one to be protected from. Everything is visible, which is
//     also what AuthMiddleware and RequestIsAdmin already do in that state.
//   - A session with NO owner is visible to nobody, including the person who
//     actually started it. Same fail-closed direction as MaskedLabel: a
//     provider that has not been taught to call SetOwner shows a generic label
//     rather than leaking one, and by the same reasoning it must not hand over
//     a transcript either. In practice the untagged sessions are spawned
//     children whose events are forwarded into the parent's stream, so the
//     person entitled to the content still has a supported way to watch it.
//   - Otherwise the owner, and only the owner. Deliberately no admin bypass:
//     these labels and events are user content (the question asked, the
//     command run), not operational data, and live_mask_test.go already
//     asserts that being an admin does not entitle you to read them.
//
// A missing session answers false as well, so a handler can give one answer to
// "no such session" and "not yours" and disclose neither.
func (m *LiveSessionMap[T]) MayView(viewer, id string) bool {
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	owner, exists := m.OwnerOf(id)
	if !exists || owner == "" {
		return false
	}
	return owner == viewer
}

// UpdateCancel replaces the cancel function for a session.
func (m *LiveSessionMap[T]) UpdateCancel(id string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Cancel = cancel
	}
}

// MarkRestoring flags the session as still being restored from the
// persistent queue; HandleCancel will reject cancels with 409 until
// ClearRestoring runs. Call this from the restore path's OnRegister
// right after registering the session.
func (m *LiveSessionMap[T]) MarkRestoring(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Restoring = true
	}
}

// ClearRestoring drops the restoring flag once the work goroutine is
// actually running. Wire it to PipelineConfig.OnStarted.
func (m *LiveSessionMap[T]) ClearRestoring(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Restoring = false
	}
}

// Get returns the session with the given ID, or nil.
func (m *LiveSessionMap[T]) Get(id string) *LiveSession[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// AppendEvent adds an event to a session's buffer. Thread-safe.
func (m *LiveSessionMap[T]) AppendEvent(id string, event T, isDone bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Events = append(s.Events, event)
		if isDone {
			s.Done = true
		}
	}
}

// UpdateStatus sets the latest status message for a session (shown in live view).
func (m *LiveSessionMap[T]) UpdateStatus(id, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Status = status
	}
}

// SnapshotEvents returns a copy of the session's events and its done status.
func (m *LiveSessionMap[T]) SnapshotEvents(id string) ([]T, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, true
	}
	cp := make([]T, len(s.Events))
	copy(cp, s.Events)
	return cp, s.Done
}

// ScheduleCleanup removes a session after 10 minutes (for reconnect window).
func (m *LiveSessionMap[T]) ScheduleCleanup(id string) {
	m.ScheduleCleanupAfter(id, 10*time.Minute)
}

// ScheduleCleanupAfter removes a session after the given duration —
// but only if it hasn't been replaced by a newer run in the meantime.
// Apps that reuse one stable id across sequential runs (e.g. a chat
// session id doubling as the run id) re-Register the same key, which
// overwrites the map entry with a fresh *LiveSession. Capturing the
// current instance here and deleting only if it's still the live one
// prevents a stale cleanup timer from yanking a later run out from
// under the client. For unique-id callers the pointer always matches,
// so this is a no-op.
func (m *LiveSessionMap[T]) ScheduleCleanupAfter(id string, d time.Duration) {
	m.mu.Lock()
	current := m.sessions[id]
	m.mu.Unlock()
	go func() {
		time.Sleep(d)
		m.mu.Lock()
		if m.sessions[id] == current {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
	}()
}

// HandleCancel returns an http.HandlerFunc that cancels a live or queued session by ID.
func (m *LiveSessionMap[T]) HandleCancel(logPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id parameter required", http.StatusBadRequest)
			return
		}
		if !m.MayView(AuthCurrentUser(r), id) {
			// 404, not 403: whether somebody else is running something is not
			// this viewer's business either.
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		m.mu.Lock()
		if s, ok := m.sessions[id]; ok && !s.Done {
			if s.Spawned {
				m.mu.Unlock()
				Log("[web] %s %s cancel rejected — spawned by parent session, cancel the parent instead", logPrefix, id)
				http.Error(w, "this session was spawned by a parent pipeline -- cancel the parent instead", http.StatusConflict)
				return
			}
			if s.Restoring {
				m.mu.Unlock()
				Log("[web] %s %s cancel rejected — session still restoring from queue", logPrefix, id)
				http.Error(w, "session is still being restored from the queue -- retry in a moment", http.StatusConflict)
				return
			}
			s.Cancel()
			s.Done = true // Mark done so it disappears from Live immediately.
			Log("[web] %s %s cancelled by user", logPrefix, id)
		}
		m.mu.Unlock()
		// Also try removing from the global queue if it was queued.
		if GlobalQueue().CancelQueued(id) {
			Log("[web] %s %s removed from queue by user", logPrefix, id)
		}
		m.ScheduleCleanup(id)
		w.WriteHeader(http.StatusOK)
	}
}

// CancelSession stops one live session and marks it done, reporting whether
// there was anything running to stop.
//
// The plain half of HandleCancel, for callers that do their own authorization
// because they can do it more precisely: a run surface knows which pipeline
// and which user a run id has to belong to, which is a narrower question than
// MayView can answer, and answering the narrow one is what keeps a global
// registry from becoming a way to reach across apps.
func (m *LiveSessionMap[T]) CancelSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.Done {
		return false
	}
	if s.Cancel != nil {
		s.Cancel()
	}
	// Marked here rather than left to the work's own exit so the session stops
	// being listed as live immediately; the goroutine unwinding on a cancelled
	// context can take as long as the call it is waiting on.
	s.Done = true
	return true
}

// ActiveSessions returns a list of currently active (not done, not queued) sessions.
func (m *LiveSessionMap[T]) ActiveSessions() []LiveEntry {
	// Get queued IDs to exclude them.
	queuedIDs := make(map[string]bool)
	for _, q := range globalQueue.QueuedEntries() {
		queuedIDs[q.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []LiveEntry
	for _, s := range m.sessions {
		if !s.Done && !queuedIDs[s.ID] {
			entries = append(entries, LiveEntry{ID: s.ID, Label: s.Label, Status: s.Status, Spawned: s.Spawned, Owner: s.Owner})
		}
	}
	return entries
}

// HandleLive returns an http.HandlerFunc that lists active and queued sessions as JSON.
//
// Labels are masked for everyone but their owner, exactly as the global
// /api/live does. This endpoint used to encode them raw, which made the
// per-app copy of the ribbon a way around the masking the global one applies:
// /research/api/live handed every user's research question to anyone with
// research access. Same entries, same reach, one of them honest about whose
// work it was showing.
func (m *LiveSessionMap[T]) HandleLive() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := m.ActiveSessions()
		entries = append(entries, GlobalQueue().QueuedEntries()...)
		viewer := AuthCurrentUser(r)
		for i := range entries {
			entries[i].Label = entries[i].MaskedLabel(viewer)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}

// HandleEvents returns an http.HandlerFunc that returns session events as JSON
// for polling-based watchers (avoids consuming an SSE connection).
func (m *LiveSessionMap[T]) HandleEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if !m.MayView(AuthCurrentUser(r), id) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		events, _ := m.SnapshotEvents(id)
		if events == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}
}

// HandleReconnect returns an http.HandlerFunc that replays buffered events
// for a reconnecting client, then streams new events until the session completes.
func (m *LiveSessionMap[T]) HandleReconnect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		// Checked ONCE, here, rather than inside the tail loop below: the
		// stream is bound to the viewer it was opened for, and ownership does
		// not change mid-session.
		if !m.MayView(AuthCurrentUser(r), id) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		events, done := m.SnapshotEvents(id)
		if events == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		sse, err := NewSSEWriter(w)
		if err != nil {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Replay buffered events.
		for _, ev := range events {
			if err := sse.Send(ev); err != nil {
				return
			}
		}
		if done {
			return
		}

		// Stream new events as they arrive. Send a keepalive comment every
		// 15 seconds of silence to prevent proxy and browser timeouts during
		// long LLM pauses. Return immediately if the client disconnects.
		const heartbeat = 15 * time.Second
		sent := len(events)
		lastActivity := time.Now()
		for {
			time.Sleep(500 * time.Millisecond)
			current, isDone := m.SnapshotEvents(id)
			if current == nil {
				return
			}
			if len(current) > sent {
				for i := sent; i < len(current); i++ {
					if err := sse.Send(current[i]); err != nil {
						return
					}
				}
				sent = len(current)
				lastActivity = time.Now()
			} else if time.Since(lastActivity) >= heartbeat {
				if err := sse.SendComment("heartbeat"); err != nil {
					return
				}
				lastActivity = time.Now()
			}
			if isDone {
				return
			}
		}
	}
}
