package timing

import (
	"testing"
	"time"
)

// Monday 2026-10-12.
func at(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.UTC) }

func half() float64 { return 0.5 }

func mustNext(t *testing.T, s Spec, from time.Time) time.Time {
	t.Helper()
	next, err := s.Next(from, half)
	if err != nil {
		t.Fatalf("%+v: %v", s, err)
	}
	return next
}

// Set times on the days ticked, and nothing on the others.
func TestTimesOnDays(t *testing.T) {
	s := Spec{Days: Weekdays, How: HowTimes, Times: []int{13 * 60, 9 * 60}}
	if got := mustNext(t, s, at(12, 10, 0)); !got.Equal(at(12, 13, 0)) {
		t.Errorf("later today: %s", got)
	}
	// Friday evening: the next is Monday morning.
	if got := mustNext(t, s, at(16, 18, 0)); !got.Equal(at(19, 9, 0)) {
		t.Errorf("over the weekend: %s", got)
	}
	if got := s.Label(); got != "weekdays at 09:00, 13:00" {
		t.Errorf("label %q", got)
	}
}

// Every N inside a window and on chosen days waits for the next opening.
func TestEveryInAWindow(t *testing.T) {
	s := Spec{Days: 1<<time.Monday | 1<<time.Wednesday, How: HowEvery, EverySeconds: 1800, HasWindow: true, WindowFrom: 9 * 60, WindowTo: 17 * 60}
	if got := mustNext(t, s, at(12, 10, 0)); !got.Equal(at(12, 10, 30)) {
		t.Errorf("inside the window: %s", got)
	}
	if got := mustNext(t, s, at(12, 16, 45)); !got.Equal(at(14, 9, 0)) {
		t.Errorf("past Monday's window, next is Wednesday's: %s", got)
	}
	if got := mustNext(t, s, at(12, 6, 0)); !got.Equal(at(12, 9, 0)) {
		t.Errorf("before the window: %s", got)
	}
	if got := s.Label(); got != "every 30 minutes between 09:00 and 17:00, Mon, Wed" {
		t.Errorf("label %q", got)
	}
	// A monitor's cadence is the same rule in seconds.
	mon := Spec{How: HowEvery, EverySeconds: 30}
	if got := mustNext(t, mon, at(12, 10, 0)); !got.Equal(at(12, 10, 0).Add(30 * time.Second)) {
		t.Errorf("every 30 seconds: %s", got)
	}
}

// Random gaps stay between their bounds, and wait for the window.
func TestRandomGap(t *testing.T) {
	s := Spec{How: HowRandomGap, MinGapSeconds: 1800, MaxGapSeconds: 3600, HasWindow: true, WindowFrom: 9 * 60, WindowTo: 17 * 60}
	got := mustNext(t, s, at(12, 11, 0))
	if got.Before(at(12, 11, 30)) || got.After(at(12, 12, 0)) {
		t.Errorf("gap out of bounds: %s", got)
	}
	late := mustNext(t, s, at(12, 16, 50))
	if late.Before(at(13, 9, 0)) || late.After(at(13, 10, 0)) {
		t.Errorf("past the window, the next lands early in the next one: %s", late)
	}
}

// N a day at random: one in each of N slots of the window.
func TestRandomCount(t *testing.T) {
	s := Spec{How: HowRandomCount, PerDay: 4, HasWindow: true, WindowFrom: 8 * 60, WindowTo: 16 * 60} // 2h slots
	if got := mustNext(t, s, at(12, 7, 0)); !got.Equal(at(12, 9, 0)) {
		t.Errorf("first slot (08-10, middle): %s", got)
	}
	if got := mustNext(t, s, at(12, 9, 0)); !got.Equal(at(12, 11, 0)) {
		t.Errorf("after a fire in slot 1, slot 2: %s", got)
	}
	if got := mustNext(t, s, at(12, 15, 0)); !got.Equal(at(13, 9, 0)) {
		t.Errorf("after the last slot, tomorrow's first: %s", got)
	}
	n := 0
	for from := at(12, 0, 0); from.Before(at(13, 0, 0)); {
		from = mustNext(t, s, from)
		if from.Before(at(13, 0, 0)) {
			n++
		}
	}
	if n != 4 {
		t.Errorf("fires in a day: %d, want 4", n)
	}
}

// Stops: a date, or a number of runs, whichever comes first.
func TestDone(t *testing.T) {
	s := Spec{How: HowDaily, Times: []int{540}, StopAt: at(20, 0, 0), MaxRuns: 5}
	if s.Done(at(19, 23, 0), 4) {
		t.Error("done before either stop")
	}
	if !s.Done(at(20, 0, 0), 0) || !s.Done(at(15, 0, 0), 5) {
		t.Error("a stop was not honoured")
	}
	if (Spec{How: HowDaily, Times: []int{540}}).Done(at(30, 0, 0), 1000) {
		t.Error("no stops is indefinitely")
	}
}

func TestValidate(t *testing.T) {
	for name, s := range map[string]Spec{
		"no how":        {},
		"no times":      {How: HowTimes},
		"two for daily": {How: HowDaily, Times: []int{1, 2}},
		"zero interval": {How: HowEvery},
		"gap inverted":  {How: HowRandomGap, MinGapSeconds: 60, MaxGapSeconds: 60},
		"window back":   {How: HowEvery, EverySeconds: 60, HasWindow: true, WindowFrom: 600, WindowTo: 500},
		"too many":      {How: HowRandomCount, PerDay: 200},
	} {
		if s.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The old "{days} HH:MM" form converts to the same fire times it always had.
func TestFromCron(t *testing.T) {
	for spec, want := range map[string]Spec{
		"daily 09:00":    {How: HowDaily, Times: []int{540}},
		"weekdays 17:00": {Days: Weekdays, How: HowDaily, Times: []int{1020}},
		"weekends 10:30": {Days: Weekends, How: HowDaily, Times: []int{630}},
		"MON,FRI 08:00":  {Days: 1<<time.Monday | 1<<time.Friday, How: HowDaily, Times: []int{480}},
	} {
		got, ok := FromCron(spec)
		if !ok || got.Days != want.Days || got.How != want.How || got.Times[0] != want.Times[0] {
			t.Errorf("%s: %+v %v", spec, got, ok)
		}
	}
	if _, ok := FromCron("DEC-25 09:00"); ok {
		t.Error("an annual date was converted; a Spec cannot say it")
	}
}
