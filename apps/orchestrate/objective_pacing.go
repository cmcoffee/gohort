// An attempt that can say when the next one should happen.
//
// See docs/objective-pacing.md. An objective's cadence is decided once, at
// creation, and until now nothing that happened afterward could change it: a
// fire that knew exactly why it fell short ("the build was still running") had
// the reason recorded, shown to the next fire, and then the next fire happened
// on the same clock anyway. Attempts were spent by the clock rather than by
// trying.
//
// core owns the tool and the ask (core/objective.go). This is the half that
// knows there IS a successor, where it is, and what the bounds are.

package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pacing"
)

// maxPacingDelay is the furthest any attempt may push its successor. A week is
// past the point where "I am waiting for something" is still the honest reading
// of a deferral; beyond it the task should be rescheduled or cancelled by its
// owner, not quietly parked by a model.
const maxPacingDelay = 7 * 24 * time.Hour

// pacingReapMargin keeps a paced fire clear of the idle reap by an hour rather
// than landing exactly on it, since the two clocks are compared at different
// moments and a tie would be decided by which one rounded.
const pacingReapMargin = time.Hour

// pacingCeiling is the furthest this task may move its own next attempt, and
// the reason, for the confirmation the model reads.
//
// Two bounds, and the second is the one that is easy to miss: the next fire is
// reaped if it arms later than the idle horizon, so an attempt allowed to pace
// past it would not be waiting, it would be deleting the task. Measured from
// now, because a fire that paced itself made a tool call and therefore renews
// the idle clock at the tail of this same run.
func pacingCeiling(idleDays int) (time.Duration, string) {
	max, why := maxPacingDelay, ""
	if idleDays > 0 {
		if reap := time.Duration(idleDays)*24*time.Hour - pacingReapMargin; reap < max {
			max = reap
			why = fmt.Sprintf("this task is dropped after %d idle day(s)", idleDays)
		}
	}
	if max < time.Minute {
		max = time.Minute
	}
	return max, why
}

// pacingAdjust is the host's shaping of a paced time: an active window still
// rules, exactly as it does for every other fire. Pacing buys a different hour,
// never an hour the owner said no to.
//
// Returns the time unchanged when the task has no window, and only ever moves
// it later, which is the contract core's NextAttemptTool relies on to report
// what the caller actually got.
func pacingAdjust(p orchUpdatePayload, now time.Time) func(time.Time) time.Time {
	if !p.HasWindow {
		return nil
	}
	return func(at time.Time) time.Time {
		return nextWindowOpen(now, at, p.WindowFromMin, p.WindowToMin)
	}
}

// pacingTool mounts the lever for this fire, or returns nothing.
//
// Mounted only for an objective on the scheduled path. A task with no `until`
// has no completion check, so "come back later" would have no way to ever stop
// and the cadence IS its whole contract; a manual Run now deliberately does not
// touch the schedule, which is that path's contract and the way an owner
// retries a stalled objective.
func pacingTool(p orchUpdatePayload, ask *pacing.Ask, reArm bool) []AgentToolDef {
	if !reArm || strings.TrimSpace(p.Until) == "" {
		return nil
	}
	now := time.Now()
	max, maxWhy := pacingCeiling(orchUpdateIdleDays())
	return []AgentToolDef{pacing.Tool(pacing.ToolSpec{
		Ask:       ask,
		Min:       orchUpdateMinInterval(),
		Max:       max,
		MaxReason: maxWhy,
		Adjust:    pacingAdjust(p, now),
		Loc:       UserLocation(p.Username),
	})}
}

// applyPacing moves the already-armed successor to the time this attempt asked
// for, and returns the line the card carries.
//
// The successor exists before the fire runs (preArmNextFire, so a crash cannot
// orphan the chain), which is why this MOVES an entry rather than scheduling
// one — the same reason a met objective has to cancel its successor rather than
// decline to create it.
//
// The payload fields are written onto `armed` for the caller to persist at the
// tail of the fire; the RunAt move is done here and immediately, because it is
// the half that matters and the half that must survive a skipped tail.
func applyPacing(p orchUpdatePayload, armed *orchUpdatePayload, armedID string, ask *pacing.Ask) string {
	at, why, ok := ask.Get()
	if !ok || armedID == "" {
		return ""
	}
	if n := ask.Count(); n > 1 {
		Log("[orchestrate/pacing] task %q asked to move its next attempt %d times; the last ask won", recurringName(p), n)
	}
	if !RescheduleTaskAt(armedID, at) {
		// The successor fired while this fire was still running (a fire that
		// outlived its own gap). Nothing to move, and nothing to repair: never
		// re-create a consumed task, that is how chains duplicate.
		Log("[orchestrate/pacing] task %q: next attempt already ran, pacing to %s skipped", recurringName(p), at.UTC().Format(time.RFC3339))
		return ""
	}
	armed.NextAttemptAt = at.UTC().Format(time.RFC3339)
	armed.NextAttemptWhy = why
	stampPacedAttempt(armed.Attempts, at)
	// Pacing is activity. Without this an attempt that spent its whole fire
	// waiting would leave the idle clock where it was, and a long enough wait
	// would reap the task it was waiting for.
	armed.LastActive = time.Now().UTC().Format(time.RFC3339)
	Log("[orchestrate/pacing] task %q moved its next attempt to %s: %s", recurringName(p), at.UTC().Format(time.RFC3339), why)
	return pacedLine(at, why, UserLocation(p.Username))
}

// pacedLine is what the card says. The time in the owner's zone, the attempt's
// own words, no interpretation: a fire that moved itself has to account for it
// where the owner is already reading.
func pacedLine(at time.Time, why string, loc *time.Location) string {
	local := at.In(loc)
	format := "15:04"
	if time.Until(at) > 20*time.Hour {
		format = "Mon 2006-01-02 15:04"
	}
	return fmt.Sprintf("next attempt %s: %s", local.Format(format), truncateObs(why, 160))
}

// stampPacedAttempt records the paced time on the attempt that asked for it.
//
// The attempt is already in the history by the time pacing runs (it is written
// with the verdict, which has to come first), so this reaches back one entry
// rather than changing that order. Without it the next fire reads that the last
// one waited and cannot tell that the wait was CHOSEN, which is the difference
// between a schedule that is slow and an attempt that is blocked.
func stampPacedAttempt(attempts []ObjectiveAttempt, at time.Time) {
	if len(attempts) == 0 {
		return
	}
	attempts[len(attempts)-1].NextAt = at.UTC().Format(time.RFC3339)
}

// standingPacingTool mounts the lever for a standing fire.
//
// No window and no reap horizon, unlike the recurring half: a standing agent
// has neither. The floor is the same deployment minimum the recurring path
// uses, because it answers the same question (how often this deployment is
// willing to do work) and is the only floor there is.
//
// Mounted on a MANUAL run too, unlike the recurring path. The runner closure is
// not told the trigger, and the objective block above already made this call
// for the bigger hammer: a goal that is met is met however the fire that met it
// was started. A fire that is blocked is blocked the same way.
func standingPacingTool(sa StandingAgent, ask *pacing.Ask) []AgentToolDef {
	if strings.TrimSpace(sa.Until) == "" {
		return nil
	}
	return []AgentToolDef{pacing.Tool(pacing.ToolSpec{
		Ask: ask,
		Min: orchUpdateMinInterval(),
		Max: maxPacingDelay,
		Loc: UserLocation(sa.Owner),
	})}
}

// applyStandingPacing writes the ask onto the record for the re-arm to find,
// and returns the line the run's summary carries.
//
// Nothing is moved here, and that is the whole difference from the recurring
// half: a standing agent's next occurrence does not exist yet while the fire
// runs. The scheduler's deferred re-arm re-reads this record and arms from it,
// so writing the time down IS the move. The caller saves the record.
func applyStandingPacing(sa *StandingAgent, ask *pacing.Ask) string {
	at, why, ok := ask.Get()
	if !ok {
		return ""
	}
	if n := ask.Count(); n > 1 {
		Log("[orchestrate/pacing] standing %s/%s asked to move its next attempt %d times; the last ask won", sa.Owner, sa.Name, n)
	}
	sa.NextAttemptAt, sa.NextAttemptWhy = at, why
	stampPacedAttempt(sa.Attempts, at)
	Log("[orchestrate/pacing] standing %s/%s moved its next attempt to %s: %s", sa.Owner, sa.Name, at.UTC().Format(time.RFC3339), why)
	return pacedLine(at, why, UserLocation(sa.Owner))
}

// sentence capitalizes a status line and closes it, for the summaries these
// lines are stitched into. They are written lower-case because most of their
// homes are mid-line (a card's detail strip), and two of them in a row need
// the punctuation to stay readable.
func sentence(line string) string {
	if line == "" {
		return ""
	}
	return strings.ToUpper(line[:1]) + line[1:] + ". "
}

// monitorPacingTool mounts the lever on the turn a monitor wakes.
//
// The asker here is not the fire, it is the AGENT the fire woke: a monitor's
// own check is a poll, with no turn in it to ask anything. So the agent reading
// "PR #12 has a new comment" is the one that can say "still in review, don't
// look again until tomorrow", which is a snooze, and is the same mechanism.
//
// Only a monitor with a goal, like the other two surfaces, and only a SCHEDULED
// one: a webhook monitor has no timer to move, so offering the tool there would
// be offering a button that does nothing.
//
// The floor is the monitor's own interval rather than a deployment minimum. A
// snooze is by definition later than the next check would have been, and asking
// for sooner than the cadence is asking for nothing (core's arming enforces the
// same rule, so the two agree).
func monitorPacingTool(m EventMonitor, ask *pacing.Ask) []AgentToolDef {
	if strings.TrimSpace(m.Until) == "" || !IsScheduledEventKind(m.Kind) {
		return nil
	}
	floor := time.Duration(m.IntervalSeconds) * time.Second
	if min := orchUpdateMinInterval(); floor < min {
		floor = min
	}
	return []AgentToolDef{pacing.Tool(pacing.ToolSpec{
		Ask: ask,
		Min: floor,
		Max: maxPacingDelay,
		Loc: UserLocation(m.Owner),
	})}
}

// applyMonitorPacing writes the ask onto the record for the tick's deferred
// re-arm to find. The caller saves.
//
// Same shape as the standing half and for the same reason: the next check does
// not exist while this one runs.
func applyMonitorPacing(m *EventMonitor, ask *pacing.Ask) string {
	at, why, ok := ask.Get()
	if !ok {
		return ""
	}
	if n := ask.Count(); n > 1 {
		Log("[orchestrate/pacing] monitor %s/%s asked to move its next check %d times; the last ask won", m.Owner, m.Name, n)
	}
	m.NextAttemptAt, m.NextAttemptWhy = at, why
	stampPacedAttempt(m.Attempts, at)
	Log("[orchestrate/pacing] monitor %s/%s moved its next check to %s: %s", m.Owner, m.Name, at.UTC().Format(time.RFC3339), why)
	return pacedLine(at, why, UserLocation(m.Owner))
}
