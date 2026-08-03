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
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error) {
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
		agentID, agentName := "", ""
		if parentID != "" {
			if pr := T.runsRegistry().Get(parentID); pr != nil {
				snap := pr.Snapshot()
				agentID, agentName = snap.AgentID, snap.AgentName
			}
		}
		if agentID == "" {
			return TaskRun{}, fmt.Errorf("no owning agent to deliver as")
		}

		ctx, cancel := context.WithCancel(withParentRun(context.Background(), parentID))
		run := T.runsRegistry().Create(sess.Username, agentID, "", cancel).
			// The agent's NAME, not just its id: the live provider falls back to
			// the id when the name is empty, so a background task would sit in
			// the pill labelled with a raw UUID — which is how a user learns
			// nothing from the one surface that was meant to tell them work is
			// still running.
			Describe("task", agentName, label).
			Parent(parentID)

		go func() {
			defer func() {
				if r := recover(); r != nil {
					run.Complete(RunStatusFailed)
					T.deliverTaskResult(sessionID, sess.Username, agentID, label, TaskProduct{}, fmt.Errorf("task panicked: %v", r))
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
//
// Attachments do NOT travel in the text. They are staged against the session
// and folded into whichever turn delivers the note (stageTaskAttachments), so
// the picture rides out on the same message that announces it. Putting them in
// the model's hands instead — "the file is at this path, attach it" — is what
// produced a finished render, an announcement that it was done, and no picture.
func (T *OrchestrateApp) deliverTaskResult(sessionID, user, agentID, label string, out TaskProduct, taskErr error) {
	var b strings.Builder
	b.WriteString(taskWakeMarker + " The work you started earlier is done: " + label + ".\n")
	if taskErr != nil {
		b.WriteString("It FAILED: " + taskErr.Error() + "\n")
		b.WriteString("Tell the user it failed and what went wrong. Do not silently retry it.")
	} else {
		b.WriteString(strings.TrimSpace(out.Text) + "\n")
		if n := stageTaskAttachments(sessionID, out); n > 0 {
			fmt.Fprintf(&b, "The %d file(s) it produced are ATTACHED to the message you are about to send — they go out with it automatically. Do not attach them again and do not describe how to find them.\n", n)
		}
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
//
// A result WITH attachments never takes the interjection route. The staged
// files are folded into the turn that carries the note, and a live turn built
// its session before this result existed — joining it would deliver the text
// and strand the picture, which is the exact failure this staging exists to
// end. It gets its own turn, where the fold is deterministic.
func (T *OrchestrateApp) wakeSessionWithNote(sessionID, user, agentID, text string) error {
	if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user && !hasStagedAttachments(sessionID) {
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
		// joining a conversation beats interrupting one. Unless files are
		// staged: see wakeSessionWithNote.
		if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user && !hasStagedAttachments(sessionID) {
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

// --- Staged attachments -----------------------------------------------------
//
// What a detached call produced, held between the moment the task finishes and
// the moment a turn delivers its result. The turn that carries the wake note
// folds these into its own session, so they ride out on the message that
// announces them.
//
// In memory on purpose. These are the bytes of a single pending delivery, not a
// record of anything: a restart loses them, and losing them is correct — the
// wake note is gone too, and the picture is still in the image space under a
// handle the user can ask for by name.

type stagedTaskMedia struct {
	at     time.Time
	images []string
	videos []string
	files  []FileAttachment
}

var (
	stagedMediaMu sync.Mutex
	stagedMedia   = map[string]*stagedTaskMedia{}
)

// stagedMediaTTL bounds how long unclaimed bytes sit in memory. Only reached
// when a wake never produced a turn at all (the agent was deleted, the session
// went away); the normal path claims within seconds of staging.
const stagedMediaTTL = 30 * time.Minute

// stageTaskAttachments holds a finished task's attachments for the session and
// reports how many were staged. Several tasks completing together accumulate —
// their notes coalesce into one turn, so their files have to as well.
func stageTaskAttachments(sessionID string, out TaskProduct) int {
	n := len(out.Images) + len(out.Videos) + len(out.Files)
	if n == 0 || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	stagedMediaMu.Lock()
	defer stagedMediaMu.Unlock()
	sweepStagedMediaLocked()
	s := stagedMedia[sessionID]
	if s == nil {
		s = &stagedTaskMedia{}
		stagedMedia[sessionID] = s
	}
	s.at = time.Now()
	s.images = append(s.images, out.Images...)
	s.videos = append(s.videos, out.Videos...)
	s.files = append(s.files, out.Files...)
	return n
}

// hasStagedAttachments reports whether anything is waiting for this session.
func hasStagedAttachments(sessionID string) bool {
	stagedMediaMu.Lock()
	defer stagedMediaMu.Unlock()
	return stagedMedia[sessionID] != nil
}

// claimTaskAttachments folds everything staged for a session into sess, so the
// reply that turn produces carries it. Appending (rather than handing the
// caller a slice to deliver itself) puts the files in the ONE place every
// surface already collects outbound attachments from — the channel reply path,
// the messaging tools, and chat's SSE flush all read sess.Images.
//
// Returns how many were folded in, for the log line that tells you delivery
// happened at all.
func claimTaskAttachments(sessionID string, sess *ToolSession) int {
	if sess == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	stagedMediaMu.Lock()
	s := stagedMedia[sessionID]
	delete(stagedMedia, sessionID)
	stagedMediaMu.Unlock()
	if s == nil {
		return 0
	}
	for _, b64 := range s.images {
		sess.AppendImage(b64)
	}
	for _, b64 := range s.videos {
		sess.AppendVideo(b64)
	}
	for _, f := range s.files {
		sess.AppendFile(f)
	}
	return len(s.images) + len(s.videos) + len(s.files)
}

func sweepStagedMediaLocked() {
	for id, s := range stagedMedia {
		if time.Since(s.at) > stagedMediaTTL {
			Log("[task] staged attachments for session %s expired unclaimed after %s", id, stagedMediaTTL)
			delete(stagedMedia, id)
		}
	}
}

// taskWakeMarker opens every wake note. The turn that carries one claims the
// session's staged files; a turn that doesn't must not, or a picture goes out
// attached to whatever unrelated thing the user happened to say next.
const taskWakeMarker = "[BACKGROUND TASK FINISHED]"

// toolCallsInclude reports whether a turn's trace contains a named tool.
func toolCallsInclude(calls []PersistedToolCall, name string) bool {
	for _, c := range calls {
		if c.Name == name {
			return true
		}
	}
	return false
}

// isTaskWake reports whether a fire's prompt is a finished task's result rather
// than a scheduled task's brief. The two share the fire path and want opposite
// treatment at three points: the framing, the no-tool-calls guard, and the
// staged attachments.
func isTaskWake(prompt string) bool { return strings.Contains(prompt, taskWakeMarker) }

// taskWakeGrace is how long a completed task waits for a live turn to pick its
// result up before starting one. A result landing mid-answer reads better than
// a second assistant message arriving on top of one already streaming.
const taskWakeGrace = 2 * time.Second

// taskWakeCoalesce is how long the first completion waits for siblings before
// starting a turn. Longer than the grace period: two renders kicked off in the
// same breath finish minutes apart, not seconds.
const taskWakeCoalesce = 15 * time.Second
