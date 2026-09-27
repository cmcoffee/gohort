package core

import (
	"testing"
	"time"
)

// A monitor with set times checks at the next of them, from the calendar,
// instead of an interval from whenever it was made.
func TestAMonitorWithSetTimesChecksAtThem(t *testing.T) {
	loc := UserLocation("")
	now := time.Date(2026, 9, 27, 3, 10, 0, 0, loc)
	m := EventMonitor{Kind: EventKindWatch, IntervalSeconds: 60, DailyAt: []int{8 * 60, 18 * 60}}
	if got, want := nextPoll(m, now), time.Date(2026, 9, 27, 8, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("next check: got %s, want %s", got, want)
	}
	late := time.Date(2026, 9, 27, 18, 2, 0, 0, loc)
	if got, want := nextPoll(m, late), time.Date(2026, 9, 28, 8, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("after the last time today, tomorrow's first: got %s, want %s", got, want)
	}
	m.DailyAt = nil
	if got := nextPoll(m, now); !got.Equal(now.Add(60 * time.Second)) {
		t.Errorf("without set times the interval applies: %s", got)
	}
}
