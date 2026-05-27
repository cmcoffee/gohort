// Mid-flight user interjections. The orchestrate runtime exposes
// HTTP endpoints that let a chat session push, edit, lock, unlock,
// or delete notes WHILE a turn is executing. The runner drains the
// queue between rounds and prepends the notes to the next LLM call.
//
// The queue PRIMITIVE itself (queue type, Push/Update/Lock/Unlock/
// Delete/Drain, the registry sync.Map, register/lookup/release
// helpers) lives in core/injection.go — servitor and phantom share
// the same machinery. This file keeps the orchestrate-specific HTTP
// handler and lightweight aliases so existing call sites in this
// package don't need to spell out core.InjectionQueue everywhere.
//
// Lifecycle:
//   - Queue registered on handleSend entry (registerInjectionQueue).
//   - Notes drained between rounds by the runner.
//   - Queue released on handleSend exit (releaseInjectionQueue).
//
// Authorization:
//   - The queue stores Owner so a second user can't write to another
//     user's session by guessing the UUID. RequireUser provides the
//     identity; the queue's Owner cross-checks.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Aliases keep existing call sites in this package terse. The
// underlying types live in core/injection.go.
type injectionQueue = InjectionQueue
type injectionNote = InjectionNote

// Wrappers preserve the lowercase helper names used throughout the
// runner. Each just forwards to the core registry.
func registerInjectionQueue(sessionID, owner, agentID string) *injectionQueue {
	return RegisterInjectionQueue(sessionID, owner, agentID)
}

func releaseInjectionQueue(sessionID string) {
	ReleaseInjectionQueue(sessionID)
}

func lookupInjectionQueue(sessionID string) *injectionQueue {
	return LookupInjectionQueue(sessionID)
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
	if q.Owner != user {
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
