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

// injectRoute is orchestrate's inject endpoint (agenthost.InjectRoute), with
// a cortex run's queue closed to new notes.
func (T *OrchestrateApp) injectRoute() http.HandlerFunc {
	return agenthost.InjectRoute{Refuse: refuseCortexNote}.Handler(T.DB)
}

// refuseCortexNote refuses a note into a cortex run: that is a write into the
// cortex, which a person reaches only by answering a question it asked.
func refuseCortexNote(q *injectionQueue, sessionID string) error {
	if sessionID == cortexSessionID(q.AgentID) {
		return jsonapi.Forbidden(cortexWriteRefusal)
	}
	return nil
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
		if strings.HasPrefix(c, taskWakeMarker) || strings.HasPrefix(c, replyWakeMarker) || strings.HasPrefix(c, strings.TrimSpace(frameworkNoteTag)) {
			msgs[i].Hidden = true
		}
	}
}
