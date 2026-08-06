// Where a knob is FILED decides whether anyone finds it.
//
// The memory relevance gate sat in "Limits" beside cron gaps and image budgets,
// while a "Memory" category already existed holding two staleness knobs. Asked
// where the gate was, the answer was a category nobody would look in — and a
// setting nobody can find is off whatever its default says.
package core

import "testing"

// Every knob that governs the fact store or the memory graph belongs together.
func TestMemoryKnobsAreFiledUnderMemory(t *testing.T) {
	want := map[string]bool{
		TunableFactGate:           true,
		TunableFactSweepThreshold: true,
		TunableFactHardCap:        true,
		TunableFactTombstoneDays:  true,
		TunableStaleVolatileDays:  true,
		TunableStaleSlowDays:      true,
		TunableGraphEntityCap:     true,
		TunableGraphEdgeCap:       true,
	}
	seen := map[string]bool{}
	for _, s := range AllTunableSpecs() {
		if !want[s.Key] {
			continue
		}
		seen[s.Key] = true
		if s.Category != "Memory" {
			t.Errorf("%s is filed under %q — a memory knob belongs under Memory", s.Key, s.Category)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("%s is not registered at all", k)
		}
	}
}

// The gate ships ON. An operator seeing it off is looking at a deliberate
// override, not the default — worth pinning, since that changed the answer to
// "should I turn it on".
func TestFactGateShipsEnabled(t *testing.T) {
	for _, s := range AllTunableSpecs() {
		if s.Key != TunableFactGate {
			continue
		}
		if s.Kind != KindBool {
			t.Errorf("the gate should render as a toggle, got kind %v", s.Kind)
		}
		if s.Default != 1 {
			t.Errorf("the gate should default to on, got %v", s.Default)
		}
		return
	}
	t.Fatal("tune_fact_gate is not registered")
}
