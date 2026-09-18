// Package pacing is the one lever an attempt has over its own schedule.
//
// An objective runs on a cadence decided once, at creation, by whoever wrote
// the task, and nothing that happened afterward could change it. So an attempt
// that knew exactly why it fell short ("the build was still running") could not
// act on it: the reason was recorded, shown to the next fire, and the next fire
// still happened an hour later because an hour is what the task was created
// with. Attempts were spent by the clock rather than by trying.
//
// The ATTEMPT decides, not the checker. The checker answers one binary question
// from evidence and does not know what the attempt is waiting for; it is also
// the cheapest call in the stack, and how often a deployment does work is not a
// lever it should hold. The attempt is the turn that just read the 401 and
// watched the queue.
//
// This package owns the ask and the tool. Whether there IS a next occurrence,
// and how to move it, belongs to whatever scheduled the work.
//
// A subpackage rather than another file in core: it is a tool and a scrap of
// state that only the surfaces mounting it need, and every exported name in
// core lands in the namespace of every file that dot-imports core.
//
// See docs/objective-pacing.md.
package pacing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core"
)

// ToolName is exported because a host has to be able to tell this call apart
// from work. The objective checker reads the tool trace as evidence, and moving
// a clock is not progress toward a goal.
const ToolName = "set_next_attempt"

// Ask is what one fire asked for, if it asked. The host owns it, reads it once
// the run is over, and decides what to do with it.
//
// Guarded: a fire's tool calls can land from more than one goroutine, and the
// last one wins by design (nothing is gained by letting a turn negotiate with
// itself), so the write has to be ordered rather than merely last.
type Ask struct {
	mu   sync.Mutex
	at   time.Time
	why  string
	set  bool
	asks int
}

// Get returns the time asked for, the reason given, and whether there was an
// ask at all. Safe on a nil receiver, so a host can read one it never mounted.
func (a *Ask) Get() (time.Time, string, bool) {
	if a == nil {
		return time.Time{}, "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.at, a.why, a.set
}

// Count is how many times the fire asked. More than one is not an error, but it
// is worth a log line at the host: the last ask won and the earlier ones did
// nothing.
func (a *Ask) Count() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asks
}

func (a *Ask) record(at time.Time, why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.at, a.why, a.set = at, why, true
	a.asks++
}

// ToolSpec is what a host supplies to mount the tool.
type ToolSpec struct {
	// Ask receives the request. Required; the host reads it after the run.
	Ask *Ask
	// Min floors the delay. An ask sooner than this is raised to it rather than
	// refused: the model asked for the earliest it could, and the deployment's
	// own minimum is the answer to that.
	Min time.Duration
	// Max ceilings it. Zero means one day, because an unbounded ceiling lets an
	// attempt park itself somewhere nobody is watching.
	Max time.Duration
	// MaxReason names what set the ceiling, for the confirmation the model
	// reads: "this task is dropped after 14 idle days" tells it something a
	// clamped timestamp does not.
	MaxReason string
	// Adjust is the host's own shaping of the final time, applied after the
	// clamps: an active window, a blackout, a quantization. Optional. It must
	// only move the time LATER, and the confirmation reports what it returns,
	// so the model is never told a time that is not the one it got.
	Adjust func(time.Time) time.Time
	// Loc renders the confirmation in the owner's zone. Nil means UTC.
	Loc *time.Location
	// Now is injected for tests. Nil means time.Now.
	Now func() time.Time
}

// maxDelay is the default ceiling when a host names none.
const maxDelay = 24 * time.Hour

const toolDescription = "Move the NEXT attempt at this goal to a later time. " +
	"Use it when this attempt cannot get further because it is WAITING on something you expect to change (a build or job still running, a deploy, a reply, an endpoint that is down, a time of day), and you have a reasonable idea of when trying again is worth it. " +
	"It moves ONE attempt. The task's normal schedule resumes after that, the goal is unchanged, and the attempt you just made still counts. " +
	"Do not call it to stop the task, to skip work you could do now, or on the attempt that reached the goal."

// NextAttemptTool mounts the one lever an attempt gets over its own schedule.
func Tool(spec ToolSpec) core.AgentToolDef {
	ask := spec.Ask
	if ask == nil {
		ask = &Ask{}
	}
	now := spec.Now
	if now == nil {
		now = time.Now
	}
	loc := spec.Loc
	if loc == nil {
		loc = time.UTC
	}
	max := spec.Max
	if max <= 0 {
		max = maxDelay
	}
	min := spec.Min
	if min < 0 {
		min = 0
	}
	if min > max {
		// A floor above the ceiling means the host has nowhere to put an
		// attempt. Collapse rather than invert, and let the clamp message say
		// the same thing twice: it is the honest answer.
		min = max
	}
	return core.AgentToolDef{
		Tool: core.Tool{
			Name:        ToolName,
			Description: toolDescription,
			Parameters: map[string]core.ToolParam{
				"minutes": {Type: "integer", Description: "How long to wait before the next attempt, in minutes. Use this unless you need a specific clock time."},
				"at":      {Type: "string", Description: "Optional exact local time instead of minutes: 'YYYY-MM-DD HH:MM' (24-hour) or a full RFC3339 timestamp."},
				"why":     {Type: "string", Description: "One short sentence: what you are waiting for. Shown to the owner and to your next attempt."},
			},
			Required: []string{"why"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			why := strings.TrimSpace(core.StringArg(args, "why"))
			if why == "" {
				return "", fmt.Errorf("say what you are waiting for: %s(minutes=60, why=\"the build is still running\")", ToolName)
			}
			start := now()
			at, err := askedTime(args, start, loc)
			if err != nil {
				return "", err
			}
			// Clamp, then let the host shape it, then report what the caller
			// actually got. Reporting the request instead would teach the model
			// that a time it never received was honoured.
			clamped := ""
			if d := at.Sub(start); d < min {
				at, clamped = start.Add(min), fmt.Sprintf("raised to the minimum gap of %s", humanDelay(min))
			} else if d > max {
				at = start.Add(max)
				clamped = "lowered to the furthest this task may be moved (" + humanDelay(max) + ")"
				if r := strings.TrimSpace(spec.MaxReason); r != "" {
					clamped += ": " + r
				}
			}
			if spec.Adjust != nil {
				if adj := spec.Adjust(at); adj.After(at) {
					at = adj
					if clamped == "" {
						clamped = "moved to the next time this task is allowed to run"
					} else {
						clamped += ", then moved to the next time this task is allowed to run"
					}
				}
			}
			ask.record(at, why)
			out := fmt.Sprintf("Next attempt moved to %s. Your reason is recorded and the owner will see it. This attempt still counts, and the task's normal schedule resumes afterward.",
				at.In(loc).Format("Mon 2006-01-02 15:04 MST"))
			if clamped != "" {
				out = fmt.Sprintf("Next attempt moved to %s (%s). Your reason is recorded and the owner will see it. This attempt still counts, and the task's normal schedule resumes afterward.",
					at.In(loc).Format("Mon 2006-01-02 15:04 MST"), clamped)
			}
			return out, nil
		},
	}
}

// nextAttemptTime reads whichever of the two forms the model used. `at` wins
// when both are present: a model that names a clock time has a reason to.
func askedTime(args map[string]any, start time.Time, loc *time.Location) (time.Time, error) {
	if raw := strings.TrimSpace(core.StringArg(args, "at")); raw != "" {
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04"} {
			if ts, err := time.ParseInLocation(layout, raw, loc); err == nil {
				if !ts.After(start) {
					return time.Time{}, fmt.Errorf("at=%q is not in the future (it is now %s)", raw, start.In(loc).Format("2006-01-02 15:04"))
				}
				return ts, nil
			}
		}
		return time.Time{}, fmt.Errorf("could not read at=%q, use 'YYYY-MM-DD HH:MM' (24-hour, your local time) or an RFC3339 timestamp, or pass minutes instead", raw)
	}
	mins := core.IntArg(args, "minutes")
	if mins <= 0 {
		return time.Time{}, fmt.Errorf("how long should the wait be? pass minutes (a positive number) or at (a local time)")
	}
	return start.Add(time.Duration(mins) * time.Minute), nil
}

// humanDelay renders a duration the way the confirmation should read it: whole
// units, because "1h30m0s" in a sentence to a model is noise it may echo.
func humanDelay(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "1 hour"
	case d >= time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return "under a minute"
}
