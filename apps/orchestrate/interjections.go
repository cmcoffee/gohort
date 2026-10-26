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
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
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

// injectRoute implements the same request/response contract servitor
// uses so the AgentLoopPanel runtime's built-in inject flow works
// unchanged:
//
//	POST   {id, text}                     → queue a new note. Returns {note_id}.
//	POST   {id, note_id, action:"lock"}   → mark a queued note as being edited.
//	POST   {id, note_id, action:"unlock"} → cancel an in-progress edit.
//	PATCH  {id, note_id, text}            → commit edited text (auto-unlocks).
//	DELETE {id, note_id}                  → remove an unread note.
//	GET    ?id=<session>                  → the notes still waiting.
//
// Notes the runner has already drained can no longer be edited or
// deleted — those return 410 Gone.
func (T *OrchestrateApp) injectRoute() http.HandlerFunc {
	return jsonapi.Methods{
		"GET": T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, _ Database) {
			agenthost.ServeWaitingNotes(w, r, user)
		}),
		"POST":   jsonapi.Post(T.DB, T.injectPost),
		"PATCH":  jsonapi.Patch(T.DB, T.injectEdit),
		"DELETE": jsonapi.Delete(T.DB, T.injectDrop),
	}.Serve
}

// injectNote is an inject request: the running session, and for a lock, an
// edit or a delete, the note.
type injectNote struct {
	ID     string `json:"id"`
	NoteID string `json:"note_id"`
	Text   string `json:"text"`
	Action string `json:"action"`
}

// errNoteGone is a note the runner has already read, or never had.
var errNoteGone = jsonapi.Status(http.StatusGone, "note already delivered or not found")

// runningQueue is the note queue of the caller's own run that in names.
// Either the session isn't running right now, or its turn already finished:
// either way there is no queue to write to.
func runningQueue(c *jsonapi.Call, in injectNote) (*injectionQueue, error) {
	if in.ID == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	q := lookupInjectionQueue(in.ID)
	if q == nil {
		return nil, jsonapi.NotFound("session not found or not interjectable")
	}
	if q.Owner != c.User {
		return nil, errNotFound
	}
	return q, nil
}

// injectPost queues a new note, or locks or unlocks one for editing.
func (T *OrchestrateApp) injectPost(c *jsonapi.Call, in injectNote) (any, error) {
	q, err := runningQueue(c, in)
	if err != nil {
		return nil, err
	}
	switch in.Action {
	case "lock", "unlock":
		if in.NoteID == "" {
			return nil, jsonapi.BadRequest("note_id required")
		}
		mark := q.Lock
		if in.Action == "unlock" {
			mark = q.Unlock
		}
		if !mark(in.NoteID) {
			return nil, errNoteGone
		}
		return jsonapi.Empty(http.StatusNoContent), nil
	case "":
	default:
		return nil, jsonapi.BadRequest("unknown action")
	}
	// A note into a cortex run is a write into the cortex, which a
	// person reaches only by answering a question it asked.
	if in.ID == cortexSessionID(q.AgentID) {
		return nil, jsonapi.Forbidden(cortexWriteRefusal)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, jsonapi.BadRequest("text required")
	}
	noteID, _ := q.Push(text)
	return map[string]string{"note_id": noteID}, nil
}

// injectEdit commits a note's edited text, which also unlocks it.
func (T *OrchestrateApp) injectEdit(c *jsonapi.Call, in injectNote) (any, error) {
	q, err := runningQueue(c, in)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Text)
	if in.NoteID == "" || text == "" {
		return nil, jsonapi.BadRequest("note_id and text required")
	}
	if !q.Update(in.NoteID, text) {
		return nil, errNoteGone
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// injectDrop removes a note the runner has not read yet.
func (T *OrchestrateApp) injectDrop(c *jsonapi.Call, in injectNote) (any, error) {
	q, err := runningQueue(c, in)
	if err != nil {
		return nil, err
	}
	if in.NoteID == "" {
		return nil, jsonapi.BadRequest("note_id required")
	}
	if !q.Delete(in.NoteID) {
		return nil, errNoteGone
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// PublicHandleInject is the landing an app routes its AgentLoopPanel's
// InjectURL to, so a message typed while a turn is running joins that turn
// instead of replacing it.
//
// Without an InjectURL the panel has nowhere to put a mid-flight message, so
// it falls through to its ordinary send path — which tears down the running
// stream and starts again. That reads as the app cancelling your work because
// you had a second thought, and it throws away everything the turn had done.
//
// Safe to expose without a check of its own: the queue is looked up by session
// id and the owner is compared against the caller, so a note can only ever
// join a run the caller started. A session that is not currently running has
// no queue and answers 404, which is the correct answer to "add this to the
// turn in progress" when there is no turn in progress.
func (T *OrchestrateApp) PublicHandleInject(w http.ResponseWriter, r *http.Request) {
	T.injectRoute()(w, r)
}

// leftoverNotesWait bounds how long a send of unread notes waits for the turn
// that left them to save them (handleSend's leftover drain, which runs as that
// turn's request ends, just after the panel hears it is done).
var leftoverNotesWait = 5 * time.Second

// awaitLeftoverNotes waits until the session's last turn has released its
// note queue, which it does only after saving any notes it left unread, and
// returns the session as it now stands.
func (T *OrchestrateApp) awaitLeftoverNotes(udb Database, agentID string, sess ChatSession) ChatSession {
	deadline := time.Now().Add(leftoverNotesWait)
	for lookupInjectionQueue(sess.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if latest, ok := loadChatSession(udb, agentID, sess.ID); ok {
		return latest
	}
	return sess
}

// trailingNotesMatch reports whether the conversation ends on the person's
// own messages whose text, joined, is text: the notes a turn saved unread.
func trailingNotesMatch(msgs []ChatMessage, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	var tail []string
	for i := len(msgs) - 1; i >= 0 && msgs[i].Role == "user"; i-- {
		tail = append([]string{strings.TrimSpace(msgs[i].Content)}, tail...)
		if strings.Join(tail, "\n\n") == text {
			return true
		}
	}
	return false
}

// hideFrameworkNotes marks, on a copy being served, the framework's own notes
// that a conversation saved as the person's messages before they were kept
// hidden: a background task's result and the framework's notes to the agent.
// The model still reads them; the transcript no longer shows them as
// something the person wrote.
func hideFrameworkNotes(msgs []ChatMessage) {
	for i := range msgs {
		if msgs[i].Role != "user" || msgs[i].Hidden {
			continue
		}
		c := strings.TrimSpace(msgs[i].Content)
		if strings.HasPrefix(c, taskWakeMarker) || strings.HasPrefix(c, strings.TrimSpace(frameworkNoteTag)) {
			msgs[i].Hidden = true
		}
	}
}
