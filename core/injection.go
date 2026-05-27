// Mid-flight injection queue — lets a user push additional notes
// into a running agent loop between rounds. The agent loop's
// OnRoundStart hook drains the queue and prepends the notes to the
// next round's history so the LLM picks them up without restarting.
//
// Originally implemented in both apps/orchestrate/interjections.go
// and apps/servitor/web.go (parallel copies). Lifted here so phantom
// (and any future caller) can reuse the same type instead of carrying
// a third copy. App-specific HTTP handlers stay in each app; they
// just back their storage with this core queue.
//
// Two-tier registry:
//
//   - Per-host-session queues (RegisterInjectionQueue / Lookup* /
//     Release*) — the existing pattern orchestrate + servitor use
//     for "notes the user typed into the chat session while a turn
//     was in flight." Keyed by the host session ID.
//
//   - Per-sub-session queues (RegisterSubSessionInjectionQueue /
//     LookupSubSessionInjectionQueue / Release*) — same shape,
//     keyed by SubSessionID. Used when a host detects a user
//     message arrived while a SubSession is in `active` state and
//     wants to push the message into the running sub-agent's
//     history without spinning up a separate dispatch.
//
// Both tiers share the same InjectionQueue type and storage map.
// The split into helper functions is just for the natural caller
// distinction; nothing prevents using one Key shape for both.
//
// Lifecycle:
//   - Caller registers a queue when starting a long-running run.
//   - User-facing endpoints (orchestrate's /api/inject, servitor's
//     equivalent) Push / Update / Delete notes.
//   - Agent loop's OnRoundStart calls Drain before each LLM round;
//     drained notes become user-role messages in the next prompt.
//   - Caller releases the queue when the run ends.

package core

import (
	"sync"
)

const (
	// MaxInjectionQueueDepth caps the per-queue note count so a
	// runaway client can't OOM the server with notes. On overflow,
	// the OLDEST entry is dropped (FIFO eviction) so the most recent
	// guidance still reaches the LLM.
	MaxInjectionQueueDepth = 25
)

// InjectionNote is one queued mid-flight note.
//
// EditLocked is set while the user is actively editing the note in
// the UI; locked notes are skipped by Drain so the agent loop
// doesn't consume an in-progress edit. The lock clears when the user
// commits (Update) or cancels (Unlock).
type InjectionNote struct {
	ID         string
	Text       string
	EditLocked bool
}

// InjectionQueue is one queue of mid-flight notes for a single
// running session (host or sub-session). All methods are safe for
// concurrent use.
//
// Owner / AgentID are optional metadata fields that app-side HTTP
// handlers use to authorize writes (Owner cross-checked against the
// requesting user). Core doesn't enforce them — apps that don't need
// authorization just leave them empty.
type InjectionQueue struct {
	mu      sync.Mutex
	notes   []InjectionNote
	Owner   string
	AgentID string
}

// Push adds a new note to the back of the queue. Returns the new
// note's ID and ok=true on success. When the queue is at
// MaxInjectionQueueDepth, the oldest note is dropped and ok=false
// signals the caller that overflow eviction happened (useful for
// surfacing a "note dropped" warning in the UI).
func (q *InjectionQueue) Push(text string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := InjectionNote{ID: UUIDv4(), Text: text}
	ok := true
	if len(q.notes) >= MaxInjectionQueueDepth {
		q.notes = q.notes[1:]
		ok = false
	}
	q.notes = append(q.notes, n)
	return n.ID, ok
}

// Update replaces a queued note's text. Implicitly unlocks the note
// (commit-after-edit pattern). Returns false when the ID isn't found
// — usually because the note was already drained.
func (q *InjectionQueue) Update(id, text string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].Text = text
			q.notes[i].EditLocked = false
			return true
		}
	}
	return false
}

// Lock marks a queued note as being edited. Locked notes are skipped
// by Drain so the agent loop doesn't consume a half-edited note.
func (q *InjectionQueue) Lock(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].EditLocked = true
			return true
		}
	}
	return false
}

// Unlock clears the edit lock on a note (cancel-edit case). The note
// stays queued and becomes eligible for the next Drain.
func (q *InjectionQueue) Unlock(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.notes {
		if q.notes[i].ID == id {
			q.notes[i].EditLocked = false
			return true
		}
	}
	return false
}

// Delete removes a queued note. No-op when the ID isn't found.
func (q *InjectionQueue) Delete(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, n := range q.notes {
		if n.ID == id {
			q.notes = append(q.notes[:i], q.notes[i+1:]...)
			return true
		}
	}
	return false
}

// Drain returns all UNLOCKED notes and removes them from the queue.
// Edit-locked notes stay queued for the next Drain. Returns nil when
// the queue is empty or all notes are locked.
func (q *InjectionQueue) Drain() []InjectionNote {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.notes) == 0 {
		return nil
	}
	var taken, kept []InjectionNote
	for _, n := range q.notes {
		if n.EditLocked {
			kept = append(kept, n)
		} else {
			taken = append(taken, n)
		}
	}
	q.notes = kept
	return taken
}

// Len reports the current queue depth. Used for UI badges.
func (q *InjectionQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.notes)
}

// --- Registry --------------------------------------------------------

// injectionQueues is the shared registry. Single sync.Map for both
// host-session and sub-session queues — keys are caller-supplied
// strings, so collisions are the caller's responsibility (use
// "host:<id>" / "sub:<id>" prefixes if both tiers might overlap).
var injectionQueues sync.Map // string → *InjectionQueue

// RegisterInjectionQueue creates a new queue and stores it under
// key. Returns the queue so the caller can hold the pointer and use
// it inline. Overwrites any existing entry under the same key
// (intentional — re-registering means "start fresh for this run").
//
// owner / agentID are optional metadata for the auth check in
// app-side HTTP handlers.
func RegisterInjectionQueue(key, owner, agentID string) *InjectionQueue {
	q := &InjectionQueue{Owner: owner, AgentID: agentID}
	injectionQueues.Store(key, q)
	return q
}

// LookupInjectionQueue returns the queue stored under key, or nil if
// no queue is registered. Apps use this to find the queue from a
// request handler.
func LookupInjectionQueue(key string) *InjectionQueue {
	if v, ok := injectionQueues.Load(key); ok {
		return v.(*InjectionQueue)
	}
	return nil
}

// ReleaseInjectionQueue drops the queue from the registry. Call at
// the end of a long-running session to free the map slot.
func ReleaseInjectionQueue(key string) {
	injectionQueues.Delete(key)
}

// --- Sub-session-keyed convenience -----------------------------------

// SubSession injection queue keys are prefixed so they don't collide
// with host-session queues that use the same UUID space.
const subSessionInjectionPrefix = "sub:"

// SubSessionInjectionQueueKey returns the registry key used by the
// SubSession-keyed helpers. Exposed so callers like RunAgentSyncContinuing
// — which accept a raw injection queue ID — can pass the right key
// without knowing the prefix shape.
func SubSessionInjectionQueueKey(subSessionID string) string {
	return subSessionInjectionPrefix + subSessionID
}

// RegisterSubSessionInjectionQueue creates a queue keyed by a
// SubSession ID. Owner is passed through for auth checks on the
// app-side HTTP handler.
func RegisterSubSessionInjectionQueue(subSessionID, owner, agentID string) *InjectionQueue {
	return RegisterInjectionQueue(subSessionInjectionPrefix+subSessionID, owner, agentID)
}

// LookupSubSessionInjectionQueue returns the SubSession's injection
// queue or nil if none is registered.
func LookupSubSessionInjectionQueue(subSessionID string) *InjectionQueue {
	return LookupInjectionQueue(subSessionInjectionPrefix + subSessionID)
}

// ReleaseSubSessionInjectionQueue drops the queue from the registry.
func ReleaseSubSessionInjectionQueue(subSessionID string) {
	ReleaseInjectionQueue(subSessionInjectionPrefix + subSessionID)
}
