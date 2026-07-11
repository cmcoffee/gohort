package core

import (
	"math"
	"testing"
	"time"
)

// TestRecencyMultiplier locks the decay curve: strength gates the effect, stable
// claims never age, volatile decays faster than slow, the down-weight floors at
// (1-strength), and future/zero AsOf never decays.
func TestRecencyMultiplier(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	daysAgo := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }

	// strength 0 → identity regardless of age/volatility.
	if m := (MemoryProvenance{Volatility: VolVolatile, AsOf: daysAgo(100)}).RecencyMultiplier(now, 0); m != 1 {
		t.Fatalf("strength 0 must be identity, got %v", m)
	}

	// Stable never decays even when ancient.
	if m := (MemoryProvenance{Volatility: VolStable, AsOf: daysAgo(3650)}).RecencyMultiplier(now, 1); m != 1 {
		t.Fatalf("stable must never decay, got %v", m)
	}

	// Volatile at exactly one half-life (default 3d): decay=0.5 → strength 1 gives
	// 1 - 1*(1-0.5) = 0.5; strength 0.3 gives 1 - 0.3*0.5 = 0.85.
	volHalf := MemoryProvenance{Volatility: VolVolatile, AsOf: daysAgo(3)}
	if m := volHalf.RecencyMultiplier(now, 1); math.Abs(m-0.5) > 1e-9 {
		t.Fatalf("volatile @1 half-life, strength 1 → 0.5, got %v", m)
	}
	if m := volHalf.RecencyMultiplier(now, 0.3); math.Abs(m-0.85) > 1e-9 {
		t.Fatalf("volatile @1 half-life, strength 0.3 → 0.85, got %v", m)
	}

	// Never below the (1-strength) floor, even at extreme age.
	if m := (MemoryProvenance{Volatility: VolVolatile, AsOf: daysAgo(100000)}).RecencyMultiplier(now, 0.4); m < 0.6-1e-9 {
		t.Fatalf("multiplier must not fall below 1-strength=0.6, got %v", m)
	}

	// Slow retains more weight than volatile at the same age.
	slow := (MemoryProvenance{Volatility: VolSlow, AsOf: daysAgo(30)}).RecencyMultiplier(now, 1)
	vol := (MemoryProvenance{Volatility: VolVolatile, AsOf: daysAgo(30)}).RecencyMultiplier(now, 1)
	if slow <= vol {
		t.Fatalf("slow should outweight volatile at 30d: slow=%v vol=%v", slow, vol)
	}

	// Future AsOf (clock skew) and zero AsOf → no decay.
	if m := (MemoryProvenance{Volatility: VolVolatile, AsOf: now.Add(48 * time.Hour)}).RecencyMultiplier(now, 1); m != 1 {
		t.Fatalf("future AsOf → 1, got %v", m)
	}
	if m := (MemoryProvenance{Volatility: VolVolatile}).RecencyMultiplier(now, 1); m != 1 {
		t.Fatalf("zero AsOf → 1, got %v", m)
	}
}
