package orchestrate

// A scheduled turn cut off by a restart.
//
// A pipeline or machine run interrupted by a restart resumes (pipeline_runs.go),
// but a scheduled agent turn was simply gone: the chain's next occurrence was
// armed before it ran, so the schedule went on, but the occurrence itself left
// no trace anywhere. A Moltbook engagement read its feed, a deploy restarted
// the server during its third model call, and its cortex showed nothing, which
// reads exactly like a run that never happened.
//
// So a fire is marked while it runs (inflightTable), and a mark still there
// from an earlier process is a fire that process never finished. On start,
// each one gets a note in the agent's cortex, and runs once more when that
// still makes sense: soon enough after it was cut off, inside its daily
// window, and not just before its next occurrence. A re-fire cut off again is
// noted but not run a third time.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// inflightTable holds a mark per scheduled fire under way.
	inflightTable = "scheduled_inflight"
	// OrchestrateScheduledRefireKind runs a cut-off occurrence once more,
	// without touching its schedule.
	OrchestrateScheduledRefireKind = "orchestrate.scheduled_refire"
	// refireWithin is how long after it started a cut-off fire is still worth
	// running: past it, the moment it was for has gone.
	refireWithin = 3 * time.Hour
	// refireBeforeNext is how close the next occurrence may be before a
	// re-fire is left to it instead.
	refireBeforeNext = 15 * time.Minute
	// refireAfter is how long after start the re-fire runs, so it does not
	// land on boot.
	refireAfter = time.Minute
)

// bootID tells this process's marks from an earlier one's.
var bootID = UUIDv4()

// inflightFire is the mark a scheduled fire leaves while it runs.
type inflightFire struct {
	ID      string            `json:"id"`
	Boot    string            `json:"boot"`
	Payload orchUpdatePayload `json:"payload"`
	// ArmedID is the next occurrence the fire armed before it ran.
	ArmedID string    `json:"armed_id,omitempty"`
	Started time.Time `json:"started"`
	// Refire marks a fire that was itself a re-fire of a cut-off one.
	Refire bool `json:"refire,omitempty"`
}

type refireCtxKey struct{}

// withRefire marks ctx as a re-fire's.
func withRefire(ctx context.Context) context.Context {
	return context.WithValue(ctx, refireCtxKey{}, true)
}

func isRefire(ctx context.Context) bool {
	v, _ := ctx.Value(refireCtxKey{}).(bool)
	return v
}

// markInflight leaves the mark for a fire under way, and returns what clears
// it when the fire ends, however it ends.
func (T *OrchestrateApp) markInflight(ctx context.Context, id string, p orchUpdatePayload, armedID string) func() {
	if T == nil || T.DB == nil || id == "" {
		return func() {}
	}
	T.DB.Set(inflightTable, id, inflightFire{ID: id, Boot: bootID, Payload: p, ArmedID: armedID, Started: time.Now(), Refire: isRefire(ctx)})
	return func() { T.DB.Unset(inflightTable, id) }
}

// recoverInterrupted settles the marks an earlier process left: a cortex note
// for each, and a re-fire where one makes sense. Run once, at start.
func (T *OrchestrateApp) recoverInterrupted() {
	if T == nil || T.DB == nil {
		return
	}
	now := time.Now()
	for _, k := range T.DB.Keys(inflightTable) {
		var m inflightFire
		if !T.DB.Get(inflightTable, k, &m) || m.Boot == bootID {
			continue
		}
		T.DB.Unset(inflightTable, k)
		p := m.Payload
		if strings.TrimSpace(p.Username) == "" || strings.TrimSpace(p.AgentID) == "" {
			continue
		}
		again, why := refireDecision(m, now, nextOccurrence(m.ArmedID))
		if again {
			if _, err := ScheduleTask(OrchestrateScheduledRefireKind, p, now.Add(refireAfter)); err != nil {
				again, why = false, fmt.Sprintf("it could not be queued again (%v)", err)
			}
		}
		note := interruptedNote(recurringName(p), m.Started.In(UserLocation(p.Username)), again, why)
		appendCortexObs(UserDB(T.DB, p.Username), p.AgentID, recurringName(p), cortexKindScheduled, note)
		Log("[orchestrate/scheduled] %q (agent=%s) was cut off by a restart (started %s): re-fire=%v %s",
			recurringName(p), p.AgentID, m.Started.Format(time.RFC3339), again, why)
	}
}

// refireDecision says whether a cut-off fire runs once more, and when it does
// not, why.
func refireDecision(m inflightFire, now time.Time, next time.Time) (bool, string) {
	p := m.Payload
	switch {
	case m.Refire:
		return false, "it was already a second try"
	case p.Paused || p.Broken:
		return false, "the task is paused"
	case now.Sub(m.Started) > refireWithin:
		return false, fmt.Sprintf("it started more than %s ago", durationWords(refireWithin))
	case !windowOpen(p, now.In(UserLocation(p.Username))):
		return false, "its window for today has closed"
	case !next.IsZero() && next.Sub(now) < refireBeforeNext:
		return false, "its next run is due in a few minutes"
	}
	return true, ""
}

// interruptedNote is the cortex line for a cut-off fire.
func interruptedNote(name string, started time.Time, again bool, why string) string {
	s := fmt.Sprintf("%s was cut off when gohort restarted (it started at %s), before it finished or reported anything.", name, started.Format("15:04"))
	if again {
		return s + " It is running once more now."
	}
	return s + " It was not run again: " + why + ". It runs at its next scheduled time."
}

// windowOpen reports whether now is inside the task's daily window, or true
// when it has none.
func windowOpen(p orchUpdatePayload, now time.Time) bool {
	has, from, to := p.HasWindow, p.WindowFromMin, p.WindowToMin
	if p.Timing.Set() {
		has, from, to = p.Timing.HasWindow, p.Timing.WindowFrom, p.Timing.WindowTo
	}
	if !has {
		return true
	}
	m := now.Hour()*60 + now.Minute()
	return m >= from && m < to
}

// nextOccurrence is when the scheduler has armed task id to run, zero when it
// is not queued.
func nextOccurrence(id string) time.Time {
	if id == "" {
		return time.Time{}
	}
	for _, t := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if t.ID == id {
			at, _ := time.Parse(time.RFC3339, t.RunAt)
			return at
		}
	}
	return time.Time{}
}

// durationWords is d in plain words ("3 hours").
func durationWords(d time.Duration) string {
	if h := int(d.Hours()); h >= 1 && d%time.Hour == 0 {
		if h == 1 {
			return "an hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	return d.Round(time.Minute).String()
}

// handleScheduledRefire runs a cut-off occurrence once more, leaving its
// schedule alone.
func handleScheduledRefire(ctx context.Context, raw json.RawMessage) {
	var p orchUpdatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		Log("[orchestrate/scheduled] re-fire payload unreadable: %v", err)
		return
	}
	err := fireOrchestrateUpdate(withRefire(ctx), p, false)
	if errors.Is(err, errSchedNotReady) {
		if _, aerr := ScheduleTask(OrchestrateScheduledRefireKind, p, time.Now().Add(2*time.Minute)); aerr != nil {
			Log("[orchestrate/scheduled] re-fire of %q could not be re-queued: %v", recurringName(p), aerr)
		}
		return
	}
	if err != nil {
		Log("[orchestrate/scheduled] re-fire of %q: %v", recurringName(p), err)
		recordScheduledDrop(p, RunFailed, fmt.Sprintf("Its second try, after a restart cut it off, did not run: %v", err))
	}
}
