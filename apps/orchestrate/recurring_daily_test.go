package orchestrate

import (
	"strings"
	"testing"
	"time"
)

// A daily task fires at its set times of day, worked out from the calendar
// each time: a late fire still leaves the next one on the hour, and a
// daylight-saving change keeps 08:00 at 08:00.
func TestADailyTaskFiresAtItsTimeOfDay(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no zoneinfo")
	}
	at := []int{8 * 60, 18*60 + 30}
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 9, 27, 2, 47, 0, 0, la), time.Date(2026, 9, 27, 8, 0, 0, 0, la)},
		{time.Date(2026, 9, 27, 8, 0, 0, 0, la), time.Date(2026, 9, 27, 18, 30, 0, 0, la)},  // on the minute: the next one
		{time.Date(2026, 9, 27, 8, 41, 0, 0, la), time.Date(2026, 9, 27, 18, 30, 0, 0, la)}, // a late fire
		{time.Date(2026, 9, 27, 23, 10, 0, 0, la), time.Date(2026, 9, 28, 8, 0, 0, 0, la)},  // tomorrow's first
		{time.Date(2026, 10, 31, 19, 0, 0, 0, la), time.Date(2026, 11, 1, 8, 0, 0, 0, la)},  // across the fall-back
		{time.Date(2027, 3, 13, 19, 0, 0, 0, la), time.Date(2027, 3, 14, 8, 0, 0, 0, la)},   // across the spring-forward
	}
	for _, c := range cases {
		if got := nextDailyFire(at, c.now); !got.Equal(c.want) {
			t.Errorf("from %s: got %s, want %s", c.now, got, c.want)
		}
	}
	p := orchUpdatePayload{Pattern: RecurringDaily, AtMinutes: at}
	if got, err := computeNextFire(&p, cases[0].now); err != nil || !got.Equal(cases[0].want) {
		t.Errorf("computeNextFire should take the daily path: %s %v", got, err)
	}
	if d := recurringDetail(p); d != "daily at 08:00, 18:30" {
		t.Errorf("label: %q", d)
	}
}

func TestDailyAtParsesAndIsValidated(t *testing.T) {
	got, err := parseDailyAt("18:30, 8:00,08:00")
	if err != nil || len(got) != 2 || got[0] != 480 || got[1] != 1110 {
		t.Fatalf("sorted and de-duplicated: %v %v", got, err)
	}
	for _, bad := range []string{"", "25:00", "8am"} {
		if _, err := parseDailyAt(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	base := RecurringSpec{SessionID: "s", AgentID: "a", Username: "u", Prompt: "news", Pattern: RecurringDaily}
	if _, err := ScheduleOrchestrateUpdate(base); err == nil || !strings.Contains(err.Error(), "daily_at") {
		t.Errorf("a daily task with no times is refused naming daily_at: %v", err)
	}
	withWindow := base
	withWindow.AtMinutes, withWindow.HasWindow, withWindow.WindowFromMin, withWindow.WindowToMin = []int{480}, true, 420, 540
	if _, err := ScheduleOrchestrateUpdate(withWindow); err == nil || !strings.Contains(err.Error(), "active_from") {
		t.Errorf("a window on a daily task is refused: %v", err)
	}
}
