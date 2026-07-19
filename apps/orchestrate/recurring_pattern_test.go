package orchestrate

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// local reference day for window math (a fixed date; the tests use the machine's
// local zone, which is what the scheduler uses in production).
func refDay(h, m int) time.Time {
	return time.Date(2026, 7, 15, h, m, 0, 0, time.Local)
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"09:00", 540, true},
		{"9:00", 540, true},
		{"17:30", 1050, true},
		{"00:00", 0, true},
		{"23:59", 1439, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"noon", 0, false},
		{"12", 0, false},
	}
	for _, c := range cases {
		got, err := parseHHMM(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseHHMM(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseHHMM(%q) = %d, nil; want error", c.in, got)
		}
	}
}

func TestNextWindowOpen(t *testing.T) {
	from, to := 540, 1020 // 09:00–17:00
	// before window → today's start
	if got := nextWindowOpen(refDay(7, 0), from, to); !got.Equal(refDay(9, 0)) {
		t.Errorf("before-window: got %v, want 09:00", got)
	}
	// inside window → unchanged
	if got := nextWindowOpen(refDay(12, 30), from, to); !got.Equal(refDay(12, 30)) {
		t.Errorf("in-window: got %v, want 12:30", got)
	}
	// past window → tomorrow's start
	got := nextWindowOpen(refDay(18, 0), from, to)
	want := refDay(9, 0).AddDate(0, 0, 1)
	if !got.Equal(want) {
		t.Errorf("past-window: got %v, want %v", got, want)
	}
}

func TestPlanRandomFireTimes_SpacingAndBounds(t *testing.T) {
	start := refDay(9, 0)
	end := refDay(17, 0)
	minGap := 30 * time.Minute
	rng := rand.New(rand.NewSource(12345))
	for iter := 0; iter < 200; iter++ {
		times, err := planRandomFireTimes(start, end, 5, minGap, rng.Float64)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", iter, err)
		}
		if len(times) != 5 {
			t.Fatalf("iter %d: got %d times, want 5", iter, len(times))
		}
		for i, ft := range times {
			if ft.Before(start) || ft.After(end) {
				t.Fatalf("iter %d: time %v out of [%v,%v]", iter, ft, start, end)
			}
			if i > 0 {
				gap := ft.Sub(times[i-1])
				if gap < minGap {
					t.Fatalf("iter %d: gap %v < minGap %v between #%d and #%d", iter, gap, minGap, i-1, i)
				}
			}
		}
	}
}

func TestPlanRandomFireTimes_WindowTooSmall(t *testing.T) {
	start := refDay(9, 0)
	end := refDay(10, 0) // 1h window
	// 5 fires * 30m spacing needs 2h of reserved gap — impossible in 1h.
	if _, err := planRandomFireTimes(start, end, 5, 30*time.Minute, rand.New(rand.NewSource(1)).Float64); err == nil {
		t.Fatal("expected error for over-packed window, got nil")
	}
}

func TestNextRandomFire_PopsQueueThenReplans(t *testing.T) {
	p := &orchUpdatePayload{
		Pattern:       RecurringRandom,
		TimesPerDay:   3,
		MinGapSeconds: 30 * 60,
		HasWindow:     true,
		WindowFromMin: 540,  // 09:00
		WindowToMin:   1020, // 17:00
	}
	seq := []float64{0.1, 0.5, 0.9}
	i := 0
	rf := func() float64 { v := seq[i%len(seq)]; i++; return v }

	now := refDay(8, 0) // before window → plans today
	first, err := nextRandomFire(p, now, rf)
	if err != nil {
		t.Fatalf("first fire: %v", err)
	}
	if !first.After(refDay(9, 0).Add(-time.Second)) || first.After(refDay(17, 0)) {
		t.Errorf("first fire %v not inside window", first)
	}
	// Two remaining should be queued.
	if len(p.RemainingToday) != 2 {
		t.Fatalf("after first pop, RemainingToday = %d, want 2", len(p.RemainingToday))
	}
	// Next fire pops from the queue (no re-plan): advance now to just past first.
	second, err := nextRandomFire(p, first.Add(time.Second), rf)
	if err != nil {
		t.Fatalf("second fire: %v", err)
	}
	if !second.After(first) {
		t.Errorf("second fire %v not after first %v", second, first)
	}
	if len(p.RemainingToday) != 1 {
		t.Fatalf("after second pop, RemainingToday = %d, want 1", len(p.RemainingToday))
	}
}

// TestNextRandomFire_MidWindowDoesNotCram is the regression for the "math only
// of what's left" bug: creating/editing a random schedule mid-window used to
// plan the whole day's fires into now→windowEnd, which crammed the spacing (or
// errored when the sliver was too small, rejecting the edit). The plan must span
// the FULL window and simply keep the fires still ahead of now.
func TestNextRandomFire_MidWindowDoesNotCram(t *testing.T) {
	// Window 09:00–17:00 (480m), 3 fires ≥30m apart — fits the full window fine.
	// "now" is 16:30, leaving only 30m: the OLD code needed 2×30m=60m of gaps in
	// that sliver and errored. The fix plans the full window instead.
	newP := func() *orchUpdatePayload {
		return &orchUpdatePayload{
			Pattern:       RecurringRandom,
			TimesPerDay:   3,
			MinGapSeconds: 30 * 60,
			HasWindow:     true,
			WindowFromMin: 540,  // 09:00
			WindowToMin:   1020, // 17:00
		}
	}
	rng := rand.New(rand.NewSource(11))
	p := newP()
	now := refDay(16, 30)
	next, err := nextRandomFire(p, now, rng.Float64)
	if err != nil {
		t.Fatalf("mid-window plan errored (the bug): %v", err)
	}
	// The next fire is either a leftover slot after 16:30 today, or (if all of
	// today's slots fell before 16:30) tomorrow's window — never before now.
	if !next.After(now) {
		t.Errorf("next fire %v is not after now %v", next, now)
	}
	// It must land inside SOME day's 09:00–17:00 window, never in the cram zone
	// beyond it.
	mins := next.Hour()*60 + next.Minute()
	if mins < 540 || mins > 1020 {
		t.Errorf("next fire %v (%d min) is outside the 09:00–17:00 window", next, mins)
	}

	// Full-window density sanity: plan from BEFORE the window and confirm all 3
	// fires span it (first can be early, last can be late) rather than clustering
	// in a tail sliver.
	p2 := newP()
	first, err := nextRandomFire(p2, refDay(8, 0), rng.Float64)
	if err != nil {
		t.Fatalf("pre-window plan: %v", err)
	}
	if len(p2.RemainingToday) != 2 {
		t.Fatalf("want 2 queued after first, got %d", len(p2.RemainingToday))
	}
	last := parseFutureTimes(p2.RemainingToday, first)
	span := last[len(last)-1].Sub(first)
	if span < time.Hour {
		t.Errorf("fires clustered (span %v) — expected them spread across the full window", span)
	}
}

func TestNextSpacedRandomFire_GapBounds(t *testing.T) {
	p := &orchUpdatePayload{
		Pattern:       RecurringRandom,
		MinGapSeconds: 20 * 60, // 20m
		MaxGapSeconds: 60 * 60, // 60m
	}
	rng := rand.New(rand.NewSource(7))
	prev := refDay(10, 0)
	for i := 0; i < 500; i++ {
		next := nextSpacedRandomFire(p, prev, rng.Float64)
		gap := next.Sub(prev)
		if gap < 20*time.Minute-time.Second || gap > 60*time.Minute+time.Second {
			t.Fatalf("iter %d: gap %v out of [20m, 60m]", i, gap)
		}
		if !next.After(prev) {
			t.Fatalf("iter %d: next %v not after prev %v", i, next, prev)
		}
		prev = next
	}
}

func TestContinuousRandom_UnlimitedFires(t *testing.T) {
	cont := orchUpdatePayload{Pattern: RecurringRandom, TimesPerDay: 0}
	if !cont.isContinuousRandom() {
		t.Fatal("times_per_day=0 random should be continuous")
	}
	if got := cont.effectiveMaxFires(); got != math.MaxInt32 {
		t.Errorf("continuous, no MaxFires → %d, want MaxInt32 (unlimited)", got)
	}
	cont.MaxFires = 10
	if got := cont.effectiveMaxFires(); got != 10 {
		t.Errorf("continuous, MaxFires=10 → %d, want 10 (honored verbatim, above nothing)", got)
	}
	nPerDay := orchUpdatePayload{Pattern: RecurringRandom, TimesPerDay: 5}
	if nPerDay.isContinuousRandom() {
		t.Fatal("N-per-day random must NOT be continuous")
	}
}

func TestEffectiveMaxFires(t *testing.T) {
	// New rule: an explicit MaxFires>0 is honored verbatim (no ceiling); 0 means
	// indefinite. The old flat fire cap was replaced by the idle guard, so a
	// task can now run forever (subject only to idle-reaping).
	if got := (orchUpdatePayload{MaxFires: 3}).effectiveMaxFires(); got != 3 {
		t.Errorf("MaxFires=3 → %d, want 3", got)
	}
	if got := (orchUpdatePayload{MaxFires: 0}).effectiveMaxFires(); got != math.MaxInt32 {
		t.Errorf("MaxFires=0 → %d, want MaxInt32 (indefinite)", got)
	}
	if got := (orchUpdatePayload{MaxFires: 5000}).effectiveMaxFires(); got != 5000 {
		t.Errorf("MaxFires=5000 → %d, want 5000 (honored verbatim, no ceiling)", got)
	}
}
