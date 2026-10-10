package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A timer is the clock as the trigger: it fires at its moment with no
// condition to test, and stops itself afterwards. Before the kind existed,
// "tell me at 1:10pm" became an http_poll against a public time API with an
// ISO datetime compared as a number, which failed every check.
func TestTimerFiresOnceAndStops(t *testing.T) {
	db := memDB(t)
	at := time.Now().Add(-time.Second)
	m := EventMonitor{Name: "notify-110pm", Owner: "craig", Kind: EventKindTimer,
		WakeBrief: "tell the user it's 1:10pm", FireAt: at, MaxFires: 1}
	SaveEventMonitor(db, m)

	var wakes []string
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		wakes = append(wakes, summary)
		return true, ""
	})
	defer RegisterEventWaker(nil)

	executeTimer(context.Background(), db, m)
	if len(wakes) != 1 {
		t.Fatalf("expected 1 wake, got %d", len(wakes))
	}
	if !strings.Contains(wakes[0], "went off") || !strings.Contains(wakes[0], "it is ") {
		t.Errorf("the wake does not say the time: %q", wakes[0])
	}
	cur, _ := GetEventMonitor(db, "craig", "notify-110pm")
	if !cur.Paused {
		t.Error("a timer that went off is still armed: it would go off again on the next re-arm")
	}
	if cur.StopReason != MonitorStopFinished {
		t.Errorf("stop reason %q, want %q: nothing broke, it finished", cur.StopReason, MonitorStopFinished)
	}
	if cur.LastFired.IsZero() {
		t.Error("LastFired not recorded")
	}
	if cur.FireCount != 1 {
		t.Errorf("fire count %d, want 1", cur.FireCount)
	}
}

// A timer edited out of its fire cap still stops after going off: an alarm
// that re-fires on every re-arm is one nobody can switch off.
func TestTimerWithoutCapStillStops(t *testing.T) {
	db := memDB(t)
	m := EventMonitor{Name: "t", Owner: "craig", Kind: EventKindTimer, FireAt: time.Now()}
	SaveEventMonitor(db, m)
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) { return true, "" })
	defer RegisterEventWaker(nil)
	executeTimer(context.Background(), db, m)
	if cur, _ := GetEventMonitor(db, "craig", "t"); !cur.Paused {
		t.Error("an uncapped timer stayed armed after going off")
	}
}

// The next check of a timer IS its moment, not now plus an interval; a moment
// already behind us is due now, so the scheduler fires it on its next pass
// rather than never.
func TestTimerSchedulesAtItsMoment(t *testing.T) {
	at := time.Now().Add(37 * time.Minute).Truncate(time.Second)
	m := EventMonitor{Kind: EventKindTimer, FireAt: at, IntervalSeconds: 900}
	if got := nextPoll(m, time.Now()); !got.Equal(at) {
		t.Errorf("next poll %s, want the timer's moment %s", got, at)
	}
	past := time.Now().Add(-time.Hour)
	m.FireAt = past
	if got := nextPoll(m, time.Now()); !got.Equal(past) {
		t.Errorf("a past moment was pushed to %s instead of being due now", got)
	}
	if !isScheduledKind(EventKindTimer) {
		t.Error("a timer is not a scheduled kind, so it would never be armed")
	}
}

// The failure this all came from: a numeric operator with a threshold that is
// not a number can never compare, so creation must refuse it instead of
// parking the monitor after three silent failures.
func TestValidateThresholdRefusesWhatCanNeverCompare(t *testing.T) {
	if err := ValidateThreshold(">=", "2026-10-10 13:10:00"); err == nil {
		t.Error("a datetime threshold under >= was accepted; every check of it fails")
	} else if !strings.Contains(err.Error(), "timer") {
		t.Errorf("the refusal does not point at the timer: %v", err)
	}
	for _, c := range []struct{ op, th string }{{">=", "150"}, {"<", " 3.5 "}, {"==", "2026-10-10"}, {"contains", "error"}} {
		if err := ValidateThreshold(c.op, c.th); err != nil {
			t.Errorf("%s %q refused: %v", c.op, c.th, err)
		}
	}
}
