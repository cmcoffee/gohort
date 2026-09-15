package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The curve is relative to the schedule's own cadence, because five minutes is
// a long wait for a five-minute task and no wait at all for a daily one.
func TestBackoffDelayDoublesAndStops(t *testing.T) {
	base := 10 * time.Minute
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, 0},
		{1, 20 * time.Minute},
		{2, 40 * time.Minute},
		{3, 80 * time.Minute},
		{4, 160 * time.Minute},
		{5, 160 * time.Minute}, // capped: past here the multiplier means "stop", which is the owner's call
		{99, 160 * time.Minute},
	}
	for _, c := range cases {
		if got := backoffDelay(base, c.streak, 0); got != c.want {
			t.Errorf("streak %d: delay %s, want %s", c.streak, got, c.want)
		}
	}
	if got := backoffDelay(base, 4, 30*time.Minute); got != 30*time.Minute {
		t.Errorf("the ceiling did not clamp the curve: %s", got)
	}
	if got := backoffDelay(0, 3, 0); got != 0 {
		t.Errorf("no cadence to multiply should be no backoff, got %s", got)
	}
}

func TestBackoffBaseReadsWhicheverScheduleThereIs(t *testing.T) {
	if got := recurringBackoffBase(orchUpdatePayload{IntervalSeconds: 600}); got != 10*time.Minute {
		t.Errorf("fixed interval: %s", got)
	}
	if got := recurringBackoffBase(orchUpdatePayload{MinGapSeconds: 1800}); got != 30*time.Minute {
		t.Errorf("random pattern should fall back to its minimum gap: %s", got)
	}
	if got := recurringBackoffBase(orchUpdatePayload{}); got != orchUpdateMinInterval() {
		t.Errorf("a payload with no cadence should fall back to the deployment minimum: %s", got)
	}

	now := time.Now()
	if got := standingBackoffBase(StandingAgent{IntervalSeconds: 900}, now); got != 15*time.Minute {
		t.Errorf("standing interval: %s", got)
	}
	// A cron's cadence is the gap to its next occurrence, so a daily job backs
	// off in days and a frequent one in minutes.
	daily := standingBackoffBase(StandingAgent{Cron: "daily 08:00"}, now)
	if daily <= 0 || daily > 24*time.Hour {
		t.Errorf("daily cron base %s is not within a day", daily)
	}
}

// The successor is already armed when a fire fails, so the backoff moves it,
// and the streak has to be persisted by this call: the failure paths return
// early by design, so the tail of the fire never runs.
func TestRecurringFailureMovesTheArmedFireAndCounts(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	PreInitScheduler()
	t.Cleanup(func() { RootDB = saved })

	p := orchUpdatePayload{Username: "u", SessionID: "s1", Prompt: "post the digest", IntervalSeconds: 600, ConsecutiveFailures: 1}
	armedID, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("arming: %v", err)
	}
	// The scheduler store outlives this test (PreInitScheduler is
	// first-writer-wins across the package), so leave the queue as we found it.
	t.Cleanup(func() { UnscheduleTask(armedID) })

	armed := p
	note := noteRecurringFailure(p, &armed, armedID, true)
	if note == "" {
		t.Fatal("a moved fire with nothing said about it is a schedule that quietly drifted")
	}
	if !strings.Contains(note, "2 time(s)") {
		t.Errorf("the note does not say this is a streak: %q", note)
	}
	if armed.ConsecutiveFailures != 2 {
		t.Fatalf("streak = %d, want 2", armed.ConsecutiveFailures)
	}
	if armed.NextAttemptWhy == "" || armed.NextAttemptAt == "" {
		t.Error("the backoff must write the same two fields an explicit ask writes, so one place explains a next run")
	}

	var moved time.Time
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if task.ID == armedID {
			moved, _ = time.Parse(time.RFC3339, task.RunAt)
		}
	}
	if moved.IsZero() {
		t.Fatal("the armed successor is gone from the queue")
	}
	// Second failure on a ten-minute task: four times the cadence.
	if d := time.Until(moved); d < 39*time.Minute || d > 41*time.Minute {
		t.Errorf("next fire is %s out, wanted about 40 minutes", d)
	}
	// And the streak survived, which is the half the next failure reads.
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if task.ID == armedID && !strings.Contains(string(task.Payload), "consecutive_failures") {
			t.Errorf("the streak was not persisted onto the armed payload: %s", task.Payload)
		}
	}
}

// An attempt that said when to come back knows something the curve does not.
// The streak still counts, so failures that each ask for a minute do not escape
// the bound entirely.
func TestAnExplicitAskBeatsTheCurve(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	PreInitScheduler()
	t.Cleanup(func() { RootDB = saved })

	p := orchUpdatePayload{Username: "u", SessionID: "s1", IntervalSeconds: 600}
	askedFor := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	armedID, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, askedFor)
	if err != nil {
		t.Fatalf("arming: %v", err)
	}
	t.Cleanup(func() { UnscheduleTask(armedID) })
	armed := p
	armed.NextAttemptAt = askedFor.UTC().Format(time.RFC3339)
	armed.NextAttemptWhy = "the build finishes at 14:00"

	if note := noteRecurringFailure(p, &armed, armedID, true); note != "" {
		t.Errorf("the curve overrode an explicit ask: %q", note)
	}
	if armed.ConsecutiveFailures != 1 {
		t.Errorf("the failure was not counted: %d", armed.ConsecutiveFailures)
	}
	if armed.NextAttemptWhy != "the build finishes at 14:00" {
		t.Error("the attempt's own reason was overwritten by the curve's")
	}
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if task.ID != armedID {
			continue
		}
		at, _ := time.Parse(time.RFC3339, task.RunAt)
		if !at.Equal(askedFor.UTC()) {
			t.Errorf("the armed fire moved off the time the attempt asked for: %s", at)
		}
	}
}

// A manual Run now has no successor to move, and must not invent one.
func TestRecurringFailureOutsideTheChainDoesNothing(t *testing.T) {
	p := orchUpdatePayload{Username: "u", IntervalSeconds: 600}
	armed := p
	if note := noteRecurringFailure(p, &armed, "", false); note != "" {
		t.Errorf("a manual run reported a backoff: %q", note)
	}
	if armed.ConsecutiveFailures != 0 {
		t.Error("a manual run counted toward the streak of the scheduled chain")
	}
}

// The standing half writes the time where the deferred re-arm will find it,
// through the same two fields pacing uses.
func TestStandingFailureBacksOffTheNextRun(t *testing.T) {
	sa := StandingAgent{Owner: "u", Name: "digest", IntervalSeconds: 3600}

	note := noteStandingFailure(&sa)
	if note == "" || !strings.Contains(note, "1 time(s)") {
		t.Fatalf("note does not name the streak: %q", note)
	}
	if sa.ConsecutiveFailures != 1 {
		t.Fatalf("streak = %d, want 1", sa.ConsecutiveFailures)
	}
	if d := time.Until(sa.NextAttemptAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("next run %s out, wanted about twice the hourly cadence", d)
	}
	if !strings.Contains(sa.NextAttemptWhy, "consecutive failure") {
		t.Errorf("the record does not say why its next run moved: %q", sa.NextAttemptWhy)
	}

	// A second failure doubles again, and the row still explains itself.
	sa.NextAttemptAt = time.Time{}
	noteStandingFailure(&sa)
	if d := time.Until(sa.NextAttemptAt); d < 239*time.Minute || d > 241*time.Minute {
		t.Errorf("second failure is %s out, wanted about four hours", d)
	}
}
