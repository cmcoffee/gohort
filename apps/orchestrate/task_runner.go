// Running a detached tool call, and delivering what it produces.
//
// core decides WHETHER to detach (a tool's own estimate against a threshold);
// this is the half that needs the run registry and the conversation, both of
// which live up here. See core/task_run.go.
//
// The run is registered with an EMPTY session id on purpose. Create cancels any
// prior run on the same session — that is what makes a fresh send abort the
// previous turn — so a task sharing the chat's session id would either kill the
// turn that spawned it or be killed by the next thing the user says. It gets its
// own entry, linked to the parent by Parent() so the live surface still nests it
// under the turn it came from.
package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *OrchestrateApp) installTaskRunner() {
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (string, error)) (TaskRun, error) {
		if sess == nil {
			return TaskRun{}, fmt.Errorf("no session")
		}
		sessionID := strings.TrimSpace(sess.ChatSessionID)
		if sessionID == "" {
			// Nowhere to deliver the result. Better to run inline than to
			// finish into a void — core falls back when this errors.
			return TaskRun{}, fmt.Errorf("no chat session to deliver into")
		}

		// The task outlives the turn, so it cannot inherit the turn's context —
		// that is cancelled the moment the turn ends. It gets its own, with the
		// parent run recorded for the live tree.
		parentID := parentRunFromCtx(sess.Context())
		// The agent that owns the conversation, read off the turn that spawned
		// us: the wake has to run as that agent, or the result arrives in the
		// thread wearing the wrong identity.
		agentID := ""
		if parentID != "" {
			if pr := T.runsRegistry().Get(parentID); pr != nil {
				agentID = pr.AgentID
			}
		}
		if agentID == "" {
			return TaskRun{}, fmt.Errorf("no owning agent to deliver as")
		}

		ctx, cancel := context.WithCancel(withParentRun(context.Background(), parentID))
		run := T.runsRegistry().Create(sess.Username, agentID, "", cancel).
			Describe("task", "", label).
			Parent(parentID)

		go func() {
			defer func() {
				if r := recover(); r != nil {
					run.Complete(RunStatusFailed)
					T.deliverTaskResult(sessionID, sess.Username, agentID, label, "", fmt.Errorf("task panicked: %v", r))
				}
			}()
			out, err := fn(ctx)
			if ctx.Err() != nil {
				// Cancelled. The user already knows they stopped it; announcing
				// it again in the conversation is noise.
				run.Complete(RunStatusCanceled)
				return
			}
			if err != nil {
				run.Complete(RunStatusFailed)
			} else {
				run.Complete(RunStatusCompleted)
			}
			T.deliverTaskResult(sessionID, sess.Username, agentID, label, out, err)
		}()

		return TaskRun{ID: run.ID, Label: label}, nil
	}
}

// deliverTaskResult wakes the conversation with what the task produced.
//
// The wake carries the ORIGINAL request as well as the result. Several turns
// may have passed, so "here is your image" with no antecedent is exactly the
// unanchored reference that gets bound to the wrong thing — the model needs to
// be told which request this answers.
func (T *OrchestrateApp) deliverTaskResult(sessionID, user, agentID, label, out string, taskErr error) {
	var b strings.Builder
	b.WriteString("[BACKGROUND TASK FINISHED] The work you started earlier is done: " + label + ".\n")
	if taskErr != nil {
		b.WriteString("It FAILED: " + taskErr.Error() + "\n")
		b.WriteString("Tell the user it failed and what went wrong. Do not silently retry it.")
	} else {
		b.WriteString(strings.TrimSpace(out) + "\n")
		b.WriteString("Deliver this to the user now, and say what it was for — several messages may have passed since they asked, so name the request rather than assuming they are still looking at it.")
	}
	// Give a live turn a moment to take it as a note before starting one of our
	// own: landing mid-answer reads better than a second message arriving on
	// top of one already streaming.
	time.Sleep(taskWakeGrace)
	if err := T.wakeSessionWithNote(sessionID, user, agentID, b.String()); err != nil {
		Warn("[task] could not deliver result into session %s: %v", sessionID, err)
	}
}

// pendingWakes buffers results for a session that has no live turn, so several
// tasks finishing together produce ONE turn instead of racing.
//
// This is the same problem the channel coalescer solves for rapid inbound
// messages, but NOT the same remedy: that one cancels and reprocesses a running
// turn with both messages folded in, which would be destructive here. A
// background result must never abort a conversation the user is having. When a
// turn IS live the note goes to the interjection queue and the turn picks it up
// between rounds; coalescing only applies to the idle case, where the
// alternative is N competing turns.
var (
	pendingWakeMu sync.Mutex
	pendingWakes  = map[string][]string{}
)

// wakeSessionWithNote delivers text into a chat session. A live turn takes it
// through the interjection queue it already drains between rounds; otherwise it
// becomes a fresh turn, merged with any sibling that lands in the same window.
func (T *OrchestrateApp) wakeSessionWithNote(sessionID, user, agentID, text string) error {
	if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user {
		q.Push(text)
		return nil
	}
	if T.bufferWake(sessionID, user, agentID, text) {
		return nil // an earlier completion owns the turn and will carry this one
	}
	return T.startWakeTurn(sessionID, user, agentID, text)
}

// startWakeTurn opens a fresh turn carrying a finished task's result.
func (T *OrchestrateApp) startWakeTurn(sessionID, user, agentID, text string) error {
	return fireOrchestrateUpdate(context.Background(), orchUpdatePayload{
		SessionID: sessionID,
		AgentID:   agentID,
		Username:  user,
		Prompt:    text,
		Name:      "background task",
		Surface:   "session",
	}, false)
}

// bufferWake records a result and reports whether someone else is already
// going to deliver it. The first caller owns the turn: it waits out the window,
// takes everything that accumulated, and sends one message.
func (T *OrchestrateApp) bufferWake(sessionID, user, agentID, text string) bool {
	pendingWakeMu.Lock()
	_, owned := pendingWakes[sessionID]
	pendingWakes[sessionID] = append(pendingWakes[sessionID], text)
	pendingWakeMu.Unlock()
	if owned {
		return true
	}
	go func() {
		time.Sleep(taskWakeCoalesce)
		pendingWakeMu.Lock()
		notes := pendingWakes[sessionID]
		delete(pendingWakes, sessionID)
		pendingWakeMu.Unlock()
		if len(notes) == 0 {
			return
		}
		// A live turn may have started during the window — prefer it, since
		// joining a conversation beats interrupting one.
		if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user {
			q.Push(joinWakeNotes(notes))
			return
		}
		if err := T.startWakeTurn(sessionID, user, agentID, joinWakeNotes(notes)); err != nil {
			Warn("[task] could not deliver %d result(s) into session %s: %v", len(notes), sessionID, err)
		}
	}()
	return false
}

// joinWakeNotes merges several finished tasks into one message. Blank-line
// joined and never numbered — the same rule the channel coalescer follows,
// because numbering invents an ordering the user did not ask about and the
// model then explains.
func joinWakeNotes(notes []string) string {
	if len(notes) == 1 {
		return notes[0]
	}
	return strings.Join(notes, "\n\n")
}

// taskWakeGrace is how long a completed task waits for a live turn to pick its
// result up before starting one. A result landing mid-answer reads better than
// a second assistant message arriving on top of one already streaming.
const taskWakeGrace = 2 * time.Second

// taskWakeCoalesce is how long the first completion waits for siblings before
// starting a turn. Longer than the grace period: two renders kicked off in the
// same breath finish minutes apart, not seconds.
const taskWakeCoalesce = 15 * time.Second
