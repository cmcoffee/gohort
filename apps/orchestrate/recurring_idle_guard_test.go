package orchestrate

import (
	"math"
	"testing"
	"time"
)

// MaxFires==0 now means indefinite for EVERY pattern — the fix for a high-
// frequency "run forever" task (e.g. 24x/day) that used to silently die on the
// flat fire cap.
func TestEffectiveMaxFiresIndefiniteEveryPattern(t *testing.T) {
	for _, p := range []orchUpdatePayload{
		{},                                          // fixed / interval
		{Pattern: RecurringRandom, TimesPerDay: 24}, // fixed-rate random (the Moltbook case)
		{Pattern: RecurringRandom},                  // continuous random
	} {
		if got := p.effectiveMaxFires(); got != math.MaxInt32 {
			t.Fatalf("MaxFires=0 must be indefinite (MaxInt32); got %d for %+v", got, p)
		}
	}
	if got := (orchUpdatePayload{MaxFires: 5}).effectiveMaxFires(); got != 5 {
		t.Fatalf("explicit MaxFires=5 must be honored; got %d", got)
	}
}

// The idle guard reaps only a task stale past idleDays, renews on LastActive,
// falls back to CreatedAt for legacy tasks, and fails safe (keeps the task) on
// a disabled guard or an unparseable/empty timestamp.
func TestIdleReapDue(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	ago := func(days int) string { return now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339) }

	check := func(name string, want bool, p orchUpdatePayload, idleDays int) {
		if got := p.idleReapDue(now, idleDays); got != want {
			t.Fatalf("%s: idleReapDue=%v, want %v", name, got, want)
		}
	}

	check("disabled guard", false, orchUpdatePayload{LastActive: ago(1000)}, 0)
	check("fresh LastActive", false, orchUpdatePayload{LastActive: ago(10)}, 90)
	check("stale LastActive", true, orchUpdatePayload{LastActive: ago(100)}, 90)
	check("legacy: old CreatedAt", true, orchUpdatePayload{CreatedAt: ago(120)}, 90)
	check("renewal beats old CreatedAt", false, orchUpdatePayload{CreatedAt: ago(120), LastActive: ago(1)}, 90)
	check("unparseable timestamp", false, orchUpdatePayload{LastActive: "garbage"}, 90)
	check("no timestamps", false, orchUpdatePayload{}, 90)
}
