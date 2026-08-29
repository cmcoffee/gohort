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
	"github.com/cmcoffee/gohort/core/prompts"
)

func (T *OrchestrateApp) installTaskRunner() {
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error) {
		if sess == nil {
			return TaskRun{}, fmt.Errorf("no session")
		}
		// The conversation this run is answering, read off the turn while it is
		// still here. Recovering it later from the session id fails exactly
		// where it matters — see orchUpdatePayload.ChannelChatID.
		originChat := strings.TrimSpace(sess.ChannelChatID)
		originHandle := strings.TrimSpace(sess.ChannelHandle)
		sessionID := sess.DeliverySession()
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

		// Findable, and stoppable, from inside the conversation. The runs
		// registry keys by user and agent, and this run deliberately carries no
		// session id — so without this nothing could answer the only question
		// the agent actually has: what did I start in HERE, and can I stop it.
		RegisterBackgroundJob(sessionID, TaskRun{ID: run.ID, Label: label}, run.Cancel)

		// The task outlives the turn that spawned it, so its spend is its
		// own — it cannot ride the turn's line, which has usually already
		// printed by the time a long render finishes. Scope it and it
		// reports when the work does. Covers the delivery wake too, since
		// that is part of what the detach costs.
		ctx, reportUsage := WithSubUsage(ctx, "task "+agentName+" "+run.ID)

		go func() {
			// First defer, so it runs LAST — after the panic recovery below
			// has had its say, and after the result is delivered.
			defer reportUsage()
			defer CompleteBackgroundJob(sessionID, run.ID)
			defer func() {
				if r := recover(); r != nil {
					run.Complete(RunStatusFailed)
					T.deliverTaskResult(taskOrigin{sessionID, sess.Username, agentID, originChat, originHandle}, label, TaskProduct{}, fmt.Errorf("task panicked: %v", r))
				}
			}()
			out, err := fn(ctx)
			if ctx.Err() != nil {
				// Cancelled. The user already knows they stopped it; announcing
				// it again in the conversation is noise.
				//
				// The SET it belonged to stops with it. Without this the button
				// lies: stop the second of four renders and the next wake
				// cheerfully starts the third, because the ledger still says
				// there are pieces left and nothing told it otherwise.
				run.Complete(RunStatusCanceled)
				if n := CloseTaskSeriesForSession(sessionID); n > 0 {
					Log("[task] cancelled run %s — closed %d set(s) still running in session %s", run.ID, n, sessionID)
				}
				return
			}
			if err != nil {
				run.Complete(RunStatusFailed)
			} else {
				run.Complete(RunStatusCompleted)
			}
			T.deliverTaskResult(taskOrigin{sessionID, sess.Username, agentID, originChat, originHandle}, label, out, err)
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
// taskOrigin is where a background task came FROM, and therefore where its
// result has to go back to. Grouped rather than passed as five arguments,
// because every hop between the detach and the delivery needs all of it.
type taskOrigin struct {
	SessionID string
	User      string
	AgentID   string
	ChatID    string // the messaging conversation, when the task began on one
	Handle    string
}

func (T *OrchestrateApp) deliverTaskResult(origin taskOrigin, label string, out TaskProduct, taskErr error) {
	// What came back, and what to do about it, kept apart. The first half is
	// FACT and is worth keeping in the thread: it names the handle for a
	// finished picture, the id of a produced file — the thing the user's next
	// message ("make it brighter") has to refer to. The second half is an
	// instruction for one turn only, and an instruction left lying in history
	// is one the model can read again three turns later and obey a second time.
	note := buildWakeNote(origin.SessionID, label, out, taskErr)
	// Give a live turn a moment to take it as a note before starting one of our
	// own: landing mid-answer reads better than a second message arriving on
	// top of one already streaming.
	time.Sleep(taskWakeGrace)
	if err := T.wakeSessionWithNote(origin, note); err != nil {
		Warn("[task] could not deliver result into session %s: %v", origin.SessionID, err)
	}
}

// buildWakeNote writes both halves of a finished task's result and stages
// whatever it attached against the session.
func buildWakeNote(sessionID, label string, out TaskProduct, taskErr error) wakeNote {
	var fact strings.Builder
	fact.WriteString(taskWakeMarker + " The work you started earlier is done: " + label + ".\n")
	act := "Deliver this to the user now, and say what it was for — several messages may have passed since they asked, so name the request rather than assuming they are still looking at it."
	if taskErr != nil {
		fact.WriteString("It FAILED: " + taskErr.Error() + "\n")
		act = "Tell the user it failed and what went wrong. Do not silently retry it."
	} else {
		fact.WriteString(strings.TrimSpace(out.Text) + "\n")
		if n := stageTaskAttachments(sessionID, out); n > 0 {
			fmt.Fprintf(&fact, "The %d file(s) it produced are ATTACHED to the message you are about to send — they go out with it automatically. Do not attach them again and do not describe how to find them.\n", n)
		}
		// More of this work to do. It joins the ACT half deliberately: the
		// count and the order belong to this one turn, and an instruction to
		// "start the next one" left in the thread is one a later turn can read
		// again and act on, long after the series finished. See
		// core.SeriesContinuation.
		if c := strings.TrimSpace(out.Continuation); c != "" {
			act += "\n" + c
		}
	}
	return wakeNote{prompt: fact.String() + act, history: strings.TrimSpace(fact.String())}
}

// wakeNote is one finished task's result in its two forms: what the delivering
// turn is asked to do, and what the thread keeps afterwards.
type wakeNote struct {
	prompt  string // reaches the model as this turn's message
	history string // persisted (hidden) so later turns can still name the result
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
	pendingWakes  = map[string][]wakeNote{}
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
func (T *OrchestrateApp) wakeSessionWithNote(origin taskOrigin, note wakeNote) error {
	sessionID, user := origin.SessionID, origin.User
	if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user && !hasStagedAttachments(sessionID) {
		q.Push(note.prompt)
		return nil
	}
	if T.bufferWake(origin, note) {
		return nil // an earlier completion owns the turn and will carry this one
	}
	return T.startWakeTurn(origin, note)
}

// startWakeTurn opens a fresh turn carrying a finished task's result.
func (T *OrchestrateApp) startWakeTurn(origin taskOrigin, note wakeNote) error {
	return fireOrchestrateUpdate(context.Background(), orchUpdatePayload{
		SessionID:     origin.SessionID,
		AgentID:       origin.AgentID,
		Username:      origin.User,
		ChannelChatID: origin.ChatID,
		ChannelHandle: origin.Handle,
		Prompt:        note.prompt,
		HistoryNote:   note.history,
		Name:          "background task",
		Surface:       "session",
	}, false)
}

// bufferWake reports whether someone else is going to deliver this result.
//
// The FIRST result opens a window and is delivered immediately by its caller —
// it is not buffered, and buffering it was the bug: the caller sent it, then
// the window closed and sent it a second time, so one finished render produced
// two announcements fifteen seconds apart and the second one promised a picture
// the first had already carried off.
//
// Anything arriving inside the window is buffered instead, and goes out as one
// further message when the window closes. That keeps a lone result prompt (the
// common case) while a burst still collapses to two messages rather than N.
func (T *OrchestrateApp) bufferWake(origin taskOrigin, note wakeNote) bool {
	sessionID, user := origin.SessionID, origin.User
	if !claimWakeWindow(sessionID, note) {
		return true // a window is open; whoever owns it will carry this one
	}
	go func() {
		time.Sleep(taskWakeCoalesce)
		notes := takeBufferedWakes(sessionID)
		if len(notes) == 0 {
			return // nothing else finished in the window — the owner already sent its own
		}
		// A live turn may have started during the window — prefer it, since
		// joining a conversation beats interrupting one. Unless files are
		// staged: see wakeSessionWithNote.
		if q := lookupInjectionQueue(sessionID); q != nil && q.Owner == user && !hasStagedAttachments(sessionID) {
			q.Push(joinWakeNotes(notes).prompt)
			return
		}
		if err := T.startWakeTurn(origin, joinWakeNotes(notes)); err != nil {
			Warn("[task] could not deliver %d result(s) into session %s: %v", len(notes), sessionID, err)
		}
	}()
	return false
}

// claimWakeWindow reports whether the caller OWNS delivery for this session —
// meaning it must send its own note now. An owner's note is deliberately not
// buffered; only results that land while its window is open are, and those go
// out together when it closes.
func claimWakeWindow(sessionID string, note wakeNote) bool {
	pendingWakeMu.Lock()
	defer pendingWakeMu.Unlock()
	if _, open := pendingWakes[sessionID]; open {
		pendingWakes[sessionID] = append(pendingWakes[sessionID], note)
		return false
	}
	// Present-but-empty marks the window open without queuing anything.
	pendingWakes[sessionID] = nil
	return true
}

// takeBufferedWakes closes the window and returns what accumulated in it.
func takeBufferedWakes(sessionID string) []wakeNote {
	pendingWakeMu.Lock()
	defer pendingWakeMu.Unlock()
	notes := pendingWakes[sessionID]
	delete(pendingWakes, sessionID)
	return notes
}

// joinWakeNotes merges several finished tasks into one message. Blank-line
// joined and never numbered — the same rule the channel coalescer follows,
// because numbering invents an ordering the user did not ask about and the
// model then explains. Both halves merge: one turn to deliver them, one record
// naming everything that came back.
func joinWakeNotes(notes []wakeNote) wakeNote {
	if len(notes) == 1 {
		return notes[0]
	}
	var prompts, history []string
	for _, n := range notes {
		prompts = append(prompts, n.prompt)
		if strings.TrimSpace(n.history) != "" {
			history = append(history, n.history)
		}
	}
	return wakeNote{
		prompt:  strings.Join(prompts, "\n\n"),
		history: strings.Join(history, "\n\n"),
	}
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

// --- Delivering a wake into the conversation it came from ----------------
//
// The scheduled-fire path this wake rides APPENDS to a stored session and
// stops. That is right for a recurring task posting into a web thread, and
// wrong for a background result whose conversation lives on a phone: the turn
// did everything correctly — attached the finished picture, wrote its line —
// and then the reply sat in the session record while the contact who asked for
// it got nothing.
//
// Observed end to end: the edit finished at 22:31:56, the wake turn attached
// 1.27 MB of PNG at 22:32:00, posted 103 characters at 22:32:01, and the
// outbox never saw any of it.

// channelTargetForWake resolves the conversation a wake should be delivered to,
// or ok=false when the session isn't a channel thread (a plain web session
// needs no send — the user is looking at it).
func channelTargetForWake(owner, agentID, sessionID string) (chatID, handle string, ok bool) {
	addr := ""
	switch {
	case strings.HasPrefix(sessionID, "chan:"):
		// Per-contact thread: the address is in the id.
		addr = strings.TrimPrefix(sessionID, "chan:")
	case sessionID == cortexSessionID(agentID):
		// The cortex home IS the channel thread for a one-channel agent — that
		// collapse is exactly what effectiveChannelSession does. With more than
		// one bound channel there is no single right recipient, and guessing
		// would send a private result to the wrong conversation.
		var bound []Channel
		for _, ch := range ListChannelsForAgent(RootDB, owner, agentID) {
			if strings.TrimSpace(ch.Service) != "" {
				bound = append(bound, ch)
			}
		}
		if len(bound) != 1 {
			return "", "", false
		}
		addr = bound[0].Address
	default:
		return "", "", false
	}
	if strings.TrimSpace(addr) == "" {
		return "", "", false
	}
	// The address may be a chat id or a bare handle; the transport knows which.
	if link, has := ActiveMessagingLink(); has {
		if sum, found := link.ResolveRecipient(owner, addr); found {
			return sum.ChatID, sum.Handle, true
		}
	}
	return addr, "", true
}

// wakeChannelTarget answers where a finished task's result should be SENT.
//
// The captured origin wins. It is the conversation the work was actually asked
// for in, recorded while the turn that started it was still running, and it is
// right in the cases derivation cannot reach: a whole-service channel binds
// with an empty Address, and a cortex agent collapses every contact into one
// home thread, so the session id names the agent rather than the person. That
// combination — the ordinary way to put an agent on a messaging service —
// resolved no recipient at all, and every background result it ever produced
// went to the transcript while the person who asked heard nothing.
//
// Derivation stays as the fallback, for a task that detached before the origin
// was carried and whose payload therefore has none.
func wakeChannelTarget(p orchUpdatePayload) (chatID, handle string, ok bool) {
	if c, h := strings.TrimSpace(p.ChannelChatID), strings.TrimSpace(p.ChannelHandle); c != "" || h != "" {
		return c, h, true
	}
	return channelTargetForWake(p.Username, p.AgentID, p.SessionID)
}

// deliverWakeToChannel sends a finished task's result out over the conversation
// that asked for it. No-op for a web session, and no-op when the turn already
// sent something itself — a model that called send_message has delivered, and a
// second copy is worse than none.
func deliverWakeToChannel(p orchUpdatePayload, subSess *ToolSession, reply string, toolTrace []PersistedToolCall) {
	if !isTaskWake(p.Prompt) || toolCallsInclude(toolTrace, "send_message") {
		return
	}
	chatID, handle, ok := wakeChannelTarget(p)
	if !ok {
		// Say so. A silent return here is what hid this for as long as it was
		// broken: the work finished, the thread showed it, and the only person
		// who could tell it had not been delivered was the one waiting for it.
		// A web session is the ordinary case and says nothing; a session that
		// LOOKS like a channel and resolved nobody is the one worth a line.
		if strings.HasPrefix(p.SessionID, "chan:") || p.SessionID == cortexSessionID(p.AgentID) {
			Log("[task] finished work for session %s has no deliverable recipient — it is in the thread but was NOT sent to the conversation", p.SessionID)
		}
		return
	}
	// House style holds here too. It used to run only on the interactive chat
	// path, so a scheduled report was the one reply nobody had read yet and the
	// one nothing corrected.
	text := strings.TrimSpace(prompts.ApplyRuleEnforcers(StripMetaTags(reply)))
	imgs, vids := collectMessageMedia(subSess, reply)
	if text == "" && len(imgs) == 0 {
		return
	}
	if _, err := operatorDeliverMedia(p.Username, p.AgentID, chatID, handle, text, imgs, vids); err != nil {
		Warn("[task] could not deliver the finished result to the conversation (%s): %v", chFirst(chatID, handle), err)
		return
	}
	Log("[task] delivered a finished background result to %s (%d char(s), %d image(s), %d video(s))",
		chFirst(chatID, handle), len(text), len(imgs), len(vids))
}
