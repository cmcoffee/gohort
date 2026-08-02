// Detached tool calls — work that outlives the turn that started it.
//
// A batch of tool calls already runs concurrently (see the agent loop's
// fan-out), so parallelism WITHIN a round is not the gap. The gap is duration:
// the batch still joins before the round ends, so one slow call holds the whole
// answer. A ComfyUI edit can take fifteen minutes, and for those fifteen minutes
// the assistant cannot say anything at all.
//
// Detaching turns that call into a RUN rather than an agent. There is nothing to
// decide while it waits — fire, wait, report — so it costs no LLM rounds; what
// it borrows from the agent machinery is the lifecycle: a registry entry, a
// cancel function, a row in the live surface nested under the turn that started
// it, and a way to deliver the result afterwards.
//
// The framework decides, not the model. A tool reports how long it expects to
// take (data it already has — an image backend knows its own poll deadline) and
// anything past the threshold detaches. Asking the model to choose would make
// detaching a judgement call it has no way to evaluate, on top of a delivery
// contract it already gets wrong.
package core

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_task_detach_threshold", Category: "Timeouts",
		Label: "Detach long tool calls after",
		Help:  "A tool call expected to take longer than this runs detached: it returns straight away, keeps working in the background, and delivers its result into the conversation when it finishes. Set high to keep everything inline.",
		// 300s clears the image-generate deadline (180s) and sits under the edit
		// one (900s), so today only edits detach.
		//
		// It is a workaround, and worth replacing. A tool's estimate is its
		// DEADLINE — how long before we give up — not how long the work takes,
		// so it over-reports by however much headroom the operator left. At 120s
		// that made every generate detach, including ones that finish in twenty
		// seconds. Tuning the threshold around a bad estimate only holds while
		// the two backends stay this far apart; measuring what a backend
		// actually takes is the fix.
		//
		// Detaching is not free either: the conversation pauses between rounds,
		// and a long enough gap lets other traffic evict its prefix from the
		// llama slot, so the wake turn re-prefills. An unnecessary detach buys a
		// cold prefill nobody asked for.
		Kind: KindSeconds, Default: 300, Min: 15, Max: 3600,
	})
}

// DetachableTool is an optional interface for tools whose calls can outlive the
// turn. ExpectedDuration reports how long THIS call is likely to take, given
// its arguments — an image tool answers with the chosen backend's own deadline.
// Zero means "no idea, keep it inline", which is the safe default for any tool
// that doesn't implement this.
//
// It is asked per CALL, not per tool: the same tool can be a two-second fetch
// and a fifteen-minute render depending on what it was handed.
type DetachableTool interface {
	ChatTool
	ExpectedDuration(args map[string]any, sess *ToolSession) time.Duration
}

// TaskRun is a detached call the framework is tracking.
type TaskRun struct {
	ID     string // handle the model can name to ask about or cancel it
	Label  string // human/model-readable summary, e.g. "editing image#1"
	Detach time.Duration
}

// TaskRunnerFunc starts fn in the background and returns a handle. The host
// owns the lifecycle: registering the run so it appears in the live surface
// under the turn that spawned it, wiring cancellation, and delivering the
// result into the conversation when fn returns.
//
// A function VARIABLE because the run registry and the conversation live in the
// app layer, which imports core rather than the other way round. Same seam as
// media.GovernedUploadFunc.
//
// Nil means detaching is unavailable and every call stays inline — the exact
// behaviour before this existed, so a host that doesn't wire it is no worse off.
var TaskRunnerFunc func(sess *ToolSession, label string, fn func() (string, error)) (TaskRun, error)

// taskDetachThreshold is the duration past which a call is detached.
func taskDetachThreshold() time.Duration { return TuneDuration("tune_task_detach_threshold") }

// ShouldDetach reports whether this call should run detached, and how long it
// is expected to take. All three conditions have to hold: a host that can run
// tasks, a tool that estimates its own duration, and an estimate past the
// threshold.
func ShouldDetach(ct ChatTool, args map[string]any, sess *ToolSession) (time.Duration, bool) {
	if TaskRunnerFunc == nil || sess == nil {
		return 0, false
	}
	d, ok := ct.(DetachableTool)
	if !ok {
		return 0, false
	}
	expected := d.ExpectedDuration(args, sess)
	if expected <= 0 {
		return 0, false
	}
	return expected, expected >= taskDetachThreshold()
}

// detachedNotice is what the model gets back INSTEAD of the result. It has to
// do two jobs the ordinary result never had to: stop it claiming the work is
// finished, and stop it re-running the call because nothing came back.
//
// The delivery contract is the part that goes wrong. A tool that returned an
// image taught the model to say "here it is"; this one must teach it to say "I
// have started it" — and a model that gets that backwards either promises an
// image that isn't there or silently redoes a fifteen-minute render.
func detachedNotice(run TaskRun, expected time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "STARTED, NOT FINISHED. This work takes about %s, so it is running in the background as task %s", humanizeTaskDuration(expected), run.ID)
	if l := strings.TrimSpace(run.Label); l != "" {
		b.WriteString(" (" + l + ")")
	}
	b.WriteString(".\n")
	b.WriteString("There is NO result yet and nothing has been delivered. Tell the user plainly that you have started it and roughly how long it takes. Do NOT describe the outcome, do NOT claim anything was sent, and do NOT call this tool again for the same request — a second call starts a second job.\n")
	b.WriteString("The result arrives on its own as a new message when it is done; you will be told then, and that is when you deliver it. Until then keep answering normally — the user can carry on talking to you while it runs.")
	return b.String()
}

// humanizeTaskDuration renders an estimate the way a person would say it, so
// the model repeats something natural rather than "900s".
func humanizeTaskDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "a moment"
	case d < 90*time.Second:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()+0.5))
	default:
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
}

// taskLabelFor names a detached run for the live surface and the wake message.
// It reads the tool's own arguments rather than taking a label parameter: every
// tool already describes its work in an argument the model wrote, and asking
// for a second, label-shaped restatement is a field authors forget.
func taskLabelFor(ct ChatTool, args map[string]any) string {
	name := ct.Name()
	if a := strings.TrimSpace(StringArg(args, "action")); a != "" {
		name += " " + a
	}
	for _, key := range []string{"prompt", "query", "message", "url"} {
		if v := strings.TrimSpace(StringArg(args, key)); v != "" {
			return name + ": " + truncateTaskLabel(v, 60)
		}
	}
	return name
}

func truncateTaskLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// errNoTaskHost is what a host returns when it cannot start a task — no chat
// session to deliver into, most often. The caller runs inline instead.
var errNoTaskHost = fmt.Errorf("no host available to run a detached task")
