// Mid-flight user interjections. While the runner is executing a turn
// (plan → step* → synthesis), the user can submit additional notes
// that get queued and prepended to the next round's context. Mirrors
// servitor's pattern so the AgentLoopPanel runtime's built-in
// InjectURL affordances work unchanged.
//
// Lifecycle:
//   - Queue is created lazily on the first inject for a session, or
//     at chat-send time if you want pre-allocation. We do the lazy
//     path so handleSend doesn't have to know about queues.
//   - Queue is removed when the session's last in-flight turn finishes
//     and the queue is empty (cleanup at end of handleSend).
//
// Authorization:
//   - The queue stores Owner so a second user can't write to another
//     user's session by guessing the UUID. RequireUser provides the
//     identity; the queue cross-checks.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// Cap the per-session queue depth so a runaway client can't OOM
	// the server with notes. Oldest entry gets dropped on overflow.
	maxInjectionQueueDepth = 25
)

// injectionNote is one queued mid-flight note. EditLocked notes are
// held out of Drain — the user is actively editing them and the
// orchestrator must not consume the note until the user finishes.
type injectionNote struct {
	ID         string
	Text       string
	EditLocked bool
}

// injectionQueue holds notes for one chat session.
type injectionQueue struct {
	mu      sync.Mutex
	notes   []injectionNote
	owner   string
	agentID string
}

func (q *injectionQueue) Push(text string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := injectionNote{ID: UUIDv4(), Text: text}
	dropped := false
	if len(q.notes) >= maxInjectionQueueDepth {
		q.notes = q.notes[1:]
		dropped = true
	}
	q.notes = append(q.notes, n)
	return n.ID, !dropped
}

func (q *injectionQueue) Update(id, text string) bool {
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

func (q *injectionQueue) Lock(id string) bool {
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

func (q *injectionQueue) Unlock(id string) bool {
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

func (q *injectionQueue) Delete(id string) bool {
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

// Drain returns all unlocked notes and removes them from the queue.
// Edit-locked notes stay queued — the orchestrator picks them up only
// after the user commits or cancels the edit.
func (q *injectionQueue) Drain() []injectionNote {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.notes) == 0 {
		return nil
	}
	var taken, kept []injectionNote
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

// injectionQueues is keyed by session id. Entries are created on
// handleSend entry (so the inject endpoint can find one) and removed
// at handleSend exit.
var injectionQueues sync.Map // sessionID -> *injectionQueue

func registerInjectionQueue(sessionID, owner, agentID string) *injectionQueue {
	q := &injectionQueue{owner: owner, agentID: agentID}
	injectionQueues.Store(sessionID, q)
	return q
}

func releaseInjectionQueue(sessionID string) {
	injectionQueues.Delete(sessionID)
}

func lookupInjectionQueue(sessionID string) *injectionQueue {
	if v, ok := injectionQueues.Load(sessionID); ok {
		return v.(*injectionQueue)
	}
	return nil
}

// handleInject implements the same request/response contract servitor
// uses so the AgentLoopPanel runtime's built-in inject flow works
// unchanged:
//
//	POST   {id, text}                     → queue a new note. Returns {note_id}.
//	POST   {id, note_id, action:"lock"}   → mark a queued note as being edited.
//	POST   {id, note_id, action:"unlock"} → cancel an in-progress edit.
//	PATCH  {id, note_id, text}            → commit edited text (auto-unlocks).
//	DELETE {id, note_id}                  → remove an unread note.
//
// Notes the runner has already drained can no longer be edited or
// deleted — those return 410 Gone.
func (T *OrchestrateApp) handleInject(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ID     string `json:"id"`
		NoteID string `json:"note_id"`
		Text   string `json:"text"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	q := lookupInjectionQueue(req.ID)
	if q == nil {
		// Either the session isn't running right now, or its turn
		// already finished. Either way, no queue to write to.
		http.Error(w, "session not found or not interjectable", http.StatusNotFound)
		return
	}
	if q.owner != user {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPost:
		switch req.Action {
		case "lock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Lock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "unlock":
			if req.NoteID == "" {
				http.Error(w, "note_id required", http.StatusBadRequest)
				return
			}
			if !q.Unlock(req.NoteID) {
				http.Error(w, "note already delivered or not found", http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "":
			// falls through to new-note path
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		noteID, _ := q.Push(req.Text)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"note_id": noteID})

	case http.MethodPatch:
		req.Text = strings.TrimSpace(req.Text)
		if req.NoteID == "" || req.Text == "" {
			http.Error(w, "note_id and text required", http.StatusBadRequest)
			return
		}
		if !q.Update(req.NoteID, req.Text) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		if req.NoteID == "" {
			http.Error(w, "note_id required", http.StatusBadRequest)
			return
		}
		if !q.Delete(req.NoteID) {
			http.Error(w, "note already delivered or not found", http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
