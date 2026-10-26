package agenthost

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// InjectRoute is an app's inject endpoint: the notes a person types while
// their turn is running, queued for the agent to read on its next decision
// (core.InjectionQueue). Every app on the AgentLoopPanel's InjectURL contract
// serves this one:
//
//	POST   {id, text}                     → queue a new note. Returns {note_id}.
//	POST   {id, note_id, action:"lock"}   → mark a queued note as being edited.
//	POST   {id, note_id, action:"unlock"} → cancel an in-progress edit.
//	PATCH  {id, note_id, text}            → commit edited text (auto-unlocks).
//	DELETE {id, note_id}                  → remove an unread note.
//	GET    ?id=<session>                  → the notes still waiting.
//
// Notes the runner has already drained can no longer be edited or deleted:
// those answer 410 Gone. Only the caller's own run is reachable; somebody
// else's answers exactly as no run does, since whether someone else is running
// something is not the caller's business.
type InjectRoute struct {
	// Refuse, when set, is asked before a new note joins a session's queue; an
	// error refuses it and is the answer.
	Refuse func(q *core.InjectionQueue, sessionID string) error
	// Queued, when set, is told of each new note, and whether the queue had
	// room for it without dropping its oldest.
	Queued func(sessionID string, clean bool)
}

// injectNote is an inject request: the running session, and for a lock, an
// edit or a delete, the note.
type injectNote struct {
	ID     string `json:"id"`
	NoteID string `json:"note_id"`
	Text   string `json:"text"`
	Action string `json:"action"`
}

var (
	// errNoRun is no running turn of the caller's under that id.
	errNoRun = jsonapi.NotFound("session not found or not interjectable")
	// errNoteGone is a note the runner has already read, or never had.
	errNoteGone = jsonapi.Status(http.StatusGone, "note already delivered or not found")
)

// Handler serves the endpoint, with callers signed in against base.
func (ir InjectRoute) Handler(base core.Database) http.HandlerFunc {
	return jsonapi.Methods{
		"GET": func(w http.ResponseWriter, r *http.Request) {
			if user, _, ok := core.RequireUser(w, r, base); ok {
				ServeWaitingNotes(w, r, user)
			}
		},
		"POST":   jsonapi.Post(base, ir.post),
		"PATCH":  jsonapi.Patch(base, ir.edit),
		"DELETE": jsonapi.Delete(base, ir.drop),
	}.Serve
}

// runningQueue is the note queue of the caller's own run that in names. A
// queue registered with no owner is nobody's.
func runningQueue(c *jsonapi.Call, in injectNote) (*core.InjectionQueue, error) {
	if in.ID == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	q := core.LookupInjectionQueue(in.ID)
	if q == nil || q.Owner == "" || q.Owner != c.User {
		return nil, errNoRun
	}
	return q, nil
}

// post queues a new note, or locks or unlocks one for editing.
func (ir InjectRoute) post(c *jsonapi.Call, in injectNote) (any, error) {
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
	if ir.Refuse != nil {
		if err := ir.Refuse(q, in.ID); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, jsonapi.BadRequest("text required")
	}
	noteID, clean := q.Push(text)
	if ir.Queued != nil {
		ir.Queued(in.ID, clean)
	}
	return map[string]string{"note_id": noteID}, nil
}

// edit commits a note's edited text, which also unlocks it.
func (ir InjectRoute) edit(c *jsonapi.Call, in injectNote) (any, error) {
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

// drop removes a note the runner has not read yet.
func (ir InjectRoute) drop(c *jsonapi.Call, in injectNote) (any, error) {
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
