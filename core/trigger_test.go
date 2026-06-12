package core

import (
	"context"
	"testing"
	"time"
)

func TestScheduledTriggerStoreRoundTrip(t *testing.T) {
	db := memDB(t)
	tr := ScheduledTrigger{
		Name: "nvda-below", Owner: "craig", Gate: GateRule, RuleMode: RuleHTTP,
		Action: ActionNotify, Notify: EventNotifyText, Token: NewEventToken(),
	}
	SaveScheduledTrigger(db, tr)

	got, ok := GetScheduledTrigger(db, "craig", "nvda-below")
	if !ok || got.Gate != GateRule || got.Token != tr.Token {
		t.Fatalf("round-trip failed: %+v ok=%v", got, ok)
	}
	byTok, ok := FindScheduledTriggerByToken(db, tr.Token)
	if !ok || byTok.Name != "nvda-below" {
		t.Fatalf("FindScheduledTriggerByToken failed: %+v ok=%v", byTok, ok)
	}
	if _, ok := GetScheduledTrigger(db, "other", "nvda-below"); ok {
		t.Fatalf("trigger leaked across owners")
	}
	if list := ListScheduledTriggers(db, "craig"); len(list) != 1 {
		t.Fatalf("ListScheduledTriggers = %d, want 1", len(list))
	}
	DeleteScheduledTrigger(db, "craig", "nvda-below")
	if _, ok := GetScheduledTrigger(db, "craig", "nvda-below"); ok {
		t.Fatalf("DeleteScheduledTrigger left the record")
	}
}

func TestNextTriggerRun(t *testing.T) {
	from := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	// Fixed interval: recurring, exactly +N seconds.
	if next, rec := nextTriggerRun(ScheduledTrigger{IntervalSeconds: 600}, from); !rec || !next.Equal(from.Add(600*time.Second)) {
		t.Errorf("interval: got %v rec=%v, want %v", next, rec, from.Add(600*time.Second))
	}
	// Interval below the floor is clamped up, not rejected.
	if next, rec := nextTriggerRun(ScheduledTrigger{IntervalSeconds: 5}, from); !rec || !next.Equal(from.Add(minTriggerInterval*time.Second)) {
		t.Errorf("sub-floor interval: got %v rec=%v, want +%ds", next, rec, minTriggerInterval)
	}
	// Cron: recurring, strictly in the future.
	if next, rec := nextTriggerRun(ScheduledTrigger{Cron: "daily 08:00"}, from); !rec || !next.After(from) {
		t.Errorf("cron: got %v rec=%v, want future", next, rec)
	}
	// Random window: recurring, strictly in the future.
	if next, rec := nextTriggerRun(ScheduledTrigger{RandomWindow: "09:00-22:00"}, from); !rec || !next.After(from) {
		t.Errorf("window: got %v rec=%v, want future", next, rec)
	}
	// One-shot (no recurrence fields): not recurring.
	if _, rec := nextTriggerRun(ScheduledTrigger{}, from); rec {
		t.Errorf("one-shot: recurring=true, want false")
	}
}

func TestScheduleTriggerNoScheduleErrors(t *testing.T) {
	db := memDB(t)
	// No run_at, no recurrence, not push -> nothing to schedule.
	err := ScheduleTrigger(db, ScheduledTrigger{Name: "x", Owner: "craig", Gate: GateAlways})
	if err == nil {
		t.Fatalf("ScheduleTrigger with no timing should error")
	}
	// Push trigger has no timer -> no error, no schedule.
	if err := ScheduleTrigger(db, ScheduledTrigger{Name: "p", Owner: "craig", Push: true}); err != nil {
		t.Fatalf("ScheduleTrigger(push) errored: %v", err)
	}
}

func TestEvaluateGateAlwaysFires(t *testing.T) {
	db := memDB(t)
	tr := ScheduledTrigger{Name: "ping", Owner: "craig", Gate: GateAlways, Action: ActionCallback, Prompt: "hi"}
	SaveScheduledTrigger(db, tr)
	fire, summary := evaluateGate(context.Background(), db, tr)
	if !fire || summary != "" {
		t.Fatalf("always gate: fire=%v summary=%q, want fire=true empty summary", fire, summary)
	}
}
