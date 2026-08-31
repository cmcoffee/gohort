// Stopping background work from inside the conversation.
//
// The user got a Stop button; the agent had nothing. "Actually, forget the
// other three" was a sentence it could only apologize to — the pictures kept
// arriving, because the thing that knew they were coming was a ledger the model
// could not see and a run it could not name.
//
// Two halves. A registry of what is running for a CONVERSATION (the runs
// registry keys by user and agent, and a detached run deliberately carries no
// session id, so neither could answer "what did I start in here?"), and a tool
// that reads it and stops things.
//
// Keyed by delivery session for the same reason delivery is: the conversation
// the work comes home to is the one it belongs to, not the sub-session a wake
// or a dispatch happened to run under. See ToolSession.DeliverySessionID.
package core

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

func init() { RegisterChatTool(&backgroundWorkTool{}) }

type backgroundJob struct {
	run    TaskRun
	cancel func()
	at     time.Time
}

var (
	bgJobsMu sync.Mutex
	bgJobs   = map[string][]backgroundJob{} // delivery session → live jobs
)

// RegisterBackgroundJob records a detached run against the conversation it will
// report back to, so the agent can find it later. cancel may be nil when the
// host cannot stop it, in which case it can still be listed.
func RegisterBackgroundJob(sessionID string, run TaskRun, cancel func()) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.TrimSpace(run.ID) == "" {
		return
	}
	bgJobsMu.Lock()
	defer bgJobsMu.Unlock()
	bgJobs[sessionID] = append(bgJobs[sessionID], backgroundJob{run: run, cancel: cancel, at: time.Now()})
}

// CompleteBackgroundJob drops a finished run. Called however it ended —
// delivered, failed or cancelled — because a list that keeps finished work is a
// list that tells the model to stop something already over.
func CompleteBackgroundJob(sessionID, runID string) {
	sessionID, runID = strings.TrimSpace(sessionID), strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return
	}
	bgJobsMu.Lock()
	defer bgJobsMu.Unlock()
	keep := bgJobs[sessionID][:0]
	for _, j := range bgJobs[sessionID] {
		if j.run.ID != runID {
			keep = append(keep, j)
		}
	}
	if len(keep) == 0 {
		delete(bgJobs, sessionID)
		return
	}
	bgJobs[sessionID] = keep
}

// ListBackgroundJobs returns what is still running for a conversation.
func ListBackgroundJobs(sessionID string) []TaskRun {
	bgJobsMu.Lock()
	defer bgJobsMu.Unlock()
	var out []TaskRun
	for _, j := range bgJobs[strings.TrimSpace(sessionID)] {
		out = append(out, j.run)
	}
	return out
}

// CancelBackgroundJobs stops work in a conversation and returns what it
// stopped. An empty runID stops everything running there, which is what
// "forget it" means and what the model will reach for.
//
// The sets stop too. Cancelling the render without closing the set it belonged
// to leaves the next wake starting the piece after it — the exact lie the Stop
// button had to be taught not to tell.
func CancelBackgroundJobs(sessionID, runID string) []TaskRun {
	sessionID, runID = strings.TrimSpace(sessionID), strings.TrimSpace(runID)
	if sessionID == "" {
		return nil
	}
	bgJobsMu.Lock()
	var stopped []TaskRun
	var cancels []func()
	keep := bgJobs[sessionID][:0]
	for _, j := range bgJobs[sessionID] {
		if runID != "" && j.run.ID != runID {
			keep = append(keep, j)
			continue
		}
		stopped = append(stopped, j.run)
		if j.cancel != nil {
			cancels = append(cancels, j.cancel)
		}
	}
	if len(keep) == 0 {
		delete(bgJobs, sessionID)
	} else {
		bgJobs[sessionID] = keep
	}
	bgJobsMu.Unlock()

	// Outside the lock: a cancel runs the host's teardown, and holding a
	// registry mutex across someone else's callback is how a stop deadlocks
	// against the completion it just triggered.
	for _, c := range cancels {
		c()
	}
	if len(stopped) > 0 {
		CloseTaskSeriesForSession(sessionID)
	}
	return stopped
}

// backgroundWorkTool lets the agent see and stop what it started.
//
// Read-and-stop only: there is no "start" here, because starting is what every
// other tool already does when the framework detaches it. This exists for the
// moment after — the user changes their mind, and the agent needs a way to act
// on that rather than narrate around it.
type backgroundWorkTool struct{}

func (t *backgroundWorkTool) Name() string { return "background_work" }

// IsFrameworkTool hides it from every tool picker.
//
// The runner force-includes this for every agent that has tools at all,
// because detaching is the framework's decision and not the agent's: work it
// started as one call keeps running after the turn whether it chose that or
// not. So the picker was offering a switch that did nothing — turn it off,
// save, and the runner adds it back on the next turn.
//
// The honest reading is that this is not a capability at all. It is the brake
// on one the framework grants unilaterally, and a list of capabilities is the
// wrong place to offer the brake: withholding it would not make an agent do
// less, it would make an agent that starts unstoppable work. The one real off
// switch stays the no-tools sentinel, where an agent has no tools to start
// anything with and so nothing to stop.
func (t *backgroundWorkTool) IsFrameworkTool() bool { return true }
func (t *backgroundWorkTool) Desc() string {
	return "See and STOP work you started that is still running in the background (a long render, a set of pictures, a dispatched agent). " +
		"actions: list (what is still running for this conversation), stop (stop it). " +
		"Use stop the moment the user changes their mind — \"actually don't\", \"forget the rest\", \"cancel that\" — instead of telling them it is on the way. " +
		"Stopping a set stops the whole set, not just the piece in flight. Nothing you stop will be delivered, and nothing further will arrive."
}
func (t *backgroundWorkTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Enum: []string{"list", "stop"}, Description: "list | stop."},
		"task": {Type: "string", Description: "(stop) Optional task id from action=\"list\". " +
			"Leave it out to stop EVERYTHING still running for this conversation, which is what \"forget it\" usually means."},
	}
}
func (t *backgroundWorkTool) Caps() []Capability { return []Capability{CapRead} }

func (t *backgroundWorkTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("background_work needs a session to know which conversation to look at")
}

func (t *backgroundWorkTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	session := sess.DeliverySession()
	if session == "" {
		return "", fmt.Errorf("background_work needs a session to know which conversation to look at")
	}
	switch strings.ToLower(strings.TrimSpace(StringArg(args, "action"))) {
	case "", "list":
		jobs := ListBackgroundJobs(session)
		if len(jobs) == 0 {
			// Said plainly, because the model's next move on an ambiguous
			// "stop that" is otherwise to claim it stopped something.
			return "Nothing is running in the background for this conversation. There is nothing to stop — if the user is waiting on something, it has already finished or was never started.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d still running:\n", len(jobs))
		for _, j := range jobs {
			b.WriteString("  " + j.ID)
			if l := strings.TrimSpace(j.Label); l != "" {
				b.WriteString(" — " + l)
			}
			b.WriteString("\n")
		}
		b.WriteString("Stop one with action=\"stop\", task=<id>, or all of them by omitting task.")
		return b.String(), nil
	case "stop":
		stopped := CancelBackgroundJobs(session, strings.TrimSpace(StringArg(args, "task")))
		if len(stopped) == 0 {
			return "Nothing was stopped — there is nothing running in the background for this conversation. Do not tell the user you cancelled something; tell them there was nothing to cancel.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Stopped %d. Nothing from it will be delivered, and no further pieces will arrive:\n", len(stopped))
		for _, j := range stopped {
			b.WriteString("  " + j.ID)
			if l := strings.TrimSpace(j.Label); l != "" {
				b.WriteString(" — " + l)
			}
			b.WriteString("\n")
		}
		b.WriteString("Say so in one line. Do NOT describe jobs, queues or how the work was running — they asked you to stop, not how it stopped.")
		return b.String(), nil
	}
	return "", fmt.Errorf("unknown action %q for background_work — use list | stop", StringArg(args, "action"))
}
