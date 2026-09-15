// Backing off a schedule that keeps failing.
//
// The sibling of docs/objective-pacing.md and deliberately a DIFFERENT feature:
// pacing is an attempt saying what it is waiting for, and this is the framework
// noticing that nothing is working. One has a reason written by something that
// knew; the other has a streak. They meet only at the end, where both move the
// next occurrence, and they share that machinery rather than growing a second.
//
// The gap: a recurring fire that errors is recorded and re-armed on the same
// cadence, forever. A task whose agent was misconfigured at 09:00 tries again at
// 09:05, and at 09:10, filing a failed run each time, until somebody notices or
// the idle reap takes it ninety days later. Event monitors already had the
// bound this adds (ConsecutiveFailures + monitorFailureThreshold parks them
// after three), which is why they are not touched here: there the streak already
// ends in a park, and backing off would only make the owner wait longer to find
// out. These two surfaces had no bound at all.
//
// An explicit ask always wins. A fire that failed but still said when to come
// back knows something the curve does not.

package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxBackoffDoublings caps the curve at 16x the schedule's own cadence. Past
// that the multiplier stops meaning "give it room" and starts meaning "stop",
// which is a decision for the owner rather than for a counter.
const maxBackoffDoublings = 4

// backoffDelay is how far out the next occurrence goes after n consecutive
// failures: double the cadence for the first, quadruple for the second, and so
// on to the cap. Zero for no failures, so the caller can treat "no backoff" and
// "no streak" as the same thing.
//
// Relative to the schedule's own cadence rather than an absolute ladder: five
// minutes is a long wait for a five-minute monitor and no wait at all for a
// daily report, and a curve that cannot tell them apart is wrong for both.
func backoffDelay(base time.Duration, streak int, ceiling time.Duration) time.Duration {
	if streak <= 0 || base <= 0 {
		return 0
	}
	if streak > maxBackoffDoublings {
		streak = maxBackoffDoublings
	}
	delay := base * time.Duration(1<<uint(streak))
	if ceiling > 0 && delay > ceiling {
		delay = ceiling
	}
	return delay
}

// recurringBackoffBase is the cadence a recurring task's backoff multiplies:
// its fixed interval, the minimum gap of a random pattern, or the deployment
// minimum when the payload says neither.
func recurringBackoffBase(p orchUpdatePayload) time.Duration {
	switch {
	case p.IntervalSeconds > 0:
		return time.Duration(p.IntervalSeconds) * time.Second
	case p.MinGapSeconds > 0:
		return time.Duration(p.MinGapSeconds) * time.Second
	}
	return orchUpdateMinInterval()
}

// standingBackoffBase is the same for a standing agent: how long until its next
// run would have been, which is the interval for an interval schedule and the
// gap to the next occurrence for a cron one.
//
// An estimate, not the arming decision — core's nextStandingRun still decides
// where the run actually lands. It only has to be the right ORDER of wait, so
// a daily job backs off in days and a five-minute one in minutes.
func standingBackoffBase(sa StandingAgent, now time.Time) time.Duration {
	if cron := strings.TrimSpace(sa.Cron); cron != "" {
		if next, err := NextCronOccurrence(cron, now); err == nil {
			if d := next.Sub(now); d > 0 {
				return d
			}
		}
	}
	if sa.IntervalSeconds > 0 {
		return time.Duration(sa.IntervalSeconds) * time.Second
	}
	return orchUpdateMinInterval()
}

// noteRecurringFailure counts this failure and pushes the already-armed
// successor out, returning the sentence the ledger row and the diag carry.
//
// It writes the payload itself rather than leaving it to the tail of the fire:
// the failure paths return early by design, so the tail never runs, and a
// streak that is not persisted is not a streak.
func noteRecurringFailure(p orchUpdatePayload, armed *orchUpdatePayload, armedID string, reArm bool) string {
	if !reArm || armedID == "" {
		return ""
	}
	armed.ConsecutiveFailures = p.ConsecutiveFailures + 1
	ceiling, _ := pacingCeiling(orchUpdateIdleDays())
	delay := backoffDelay(recurringBackoffBase(p), armed.ConsecutiveFailures, ceiling)
	if delay <= 0 {
		UpdateScheduledTaskPayload(armedID, *armed)
		return ""
	}
	at := time.Now().Add(delay)
	// An attempt that said when to come back beats the curve, and has already
	// moved the successor by the time this runs. Leave its time alone; the
	// streak still counts, so a run of failures that each ask for a minute do
	// not escape the bound entirely.
	if armed.NextAttemptAt != "" {
		UpdateScheduledTaskPayload(armedID, *armed)
		return ""
	}
	if !RescheduleTaskAt(armedID, at) {
		Log("[orchestrate/backoff] task %q: next fire already ran — backoff skipped", recurringName(p))
		return ""
	}
	// Same two fields an explicit ask writes, so every surface that explains a
	// next run reads ONE place and does not care which of the two moved it.
	armed.NextAttemptAt = at.UTC().Format(time.RFC3339)
	armed.NextAttemptWhy = fmt.Sprintf("backed off after %d consecutive failure(s)", armed.ConsecutiveFailures)
	UpdateScheduledTaskPayload(armedID, *armed)
	Log("[orchestrate/backoff] task %q failed %d time(s) in a row — next fire moved to %s",
		recurringName(p), armed.ConsecutiveFailures, at.UTC().Format(time.RFC3339))
	return backoffNote(armed.ConsecutiveFailures, at, UserLocation(p.Username))
}

// noteStandingFailure counts this failure on the record and writes the backed-off
// time where the deferred re-arm will find it. The caller saves.
func noteStandingFailure(sa *StandingAgent) string {
	sa.ConsecutiveFailures++
	now := time.Now()
	delay := backoffDelay(standingBackoffBase(*sa, now), sa.ConsecutiveFailures, maxPacingDelay)
	if delay <= 0 || !sa.NextAttemptAt.IsZero() {
		return "" // no curve to apply, or an explicit ask already owns the slot
	}
	at := now.Add(delay)
	sa.NextAttemptAt = at
	sa.NextAttemptWhy = fmt.Sprintf("backed off after %d consecutive failure(s)", sa.ConsecutiveFailures)
	Log("[orchestrate/backoff] standing %s/%s failed %d time(s) in a row — next run moved to %s",
		sa.Owner, sa.Name, sa.ConsecutiveFailures, at.UTC().Format(time.RFC3339))
	return backoffNote(sa.ConsecutiveFailures, at, UserLocation(sa.Owner))
}

// backoffNote is what the owner reads. Both halves matter: the streak says this
// is not a one-off, and the time says when to expect the next word, so a
// schedule that has gone quiet can be told apart from one that has stopped.
func backoffNote(streak int, at time.Time, loc *time.Location) string {
	return fmt.Sprintf(" Failed %d time(s) in a row, so the next run is backed off to %s — fix what the error names and it returns to its normal schedule on the first run that works.",
		streak, at.In(loc).Format("Mon 2006-01-02 15:04"))
}
