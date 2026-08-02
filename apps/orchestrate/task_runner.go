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
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *OrchestrateApp) installTaskRunner() {
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func() (string, error)) (TaskRun, error) {
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
			out, err := fn()
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

// wakeSessionWithNote delivers text into a chat session. A live turn takes it
// through the interjection queue it already drains between rounds; otherwise it
// becomes a fresh turn.
func (T *OrchestrateApp) wakeSessionWithNote(sessionID, user, agentID, text string) error {
	if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user {
		q.Push(text)
		return nil
	}
	return fireOrchestrateUpdate(context.Background(), orchUpdatePayload{
		SessionID: sessionID,
		AgentID:   agentID,
		Username:  user,
		Prompt:    text,
		Name:      "background task",
		Surface:   "session",
	}, false)
}

// taskWakeGrace is how long a completed task waits for a live turn to pick its
// result up before starting one. A result landing mid-answer reads better than
// a second assistant message arriving on top of one already streaming.
const taskWakeGrace = 2 * time.Second
