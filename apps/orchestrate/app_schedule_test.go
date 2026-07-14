package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestAppActionDefs_Schedule covers the self-update cadence parsing on an
// action's optional `schedule` object: interval floor, cron/interval clash,
// max_idle_days passthrough, and a schedule that names no cadence (no-op).
func TestAppActionDefs_Schedule(t *testing.T) {
	acts, notes := appActionDefs([]any{
		// 0: below the floor → floored, with a note.
		map[string]any{"name": "refresh", "script": "print('{}')",
			"schedule": map[string]any{"interval_seconds": float64(60), "max_idle_days": float64(7)}},
		// 1: cron + interval → cron wins, interval dropped, with a note.
		map[string]any{"name": "daily", "script": "print('{}')",
			"schedule": map[string]any{"cron": "MON 09:00", "interval_seconds": float64(3600)}},
		// 2: schedule object with no cadence → no schedule, with a note.
		map[string]any{"name": "empty_sched", "script": "print('{}')",
			"schedule": map[string]any{"max_idle_days": float64(3)}},
		// 3: no schedule at all → manual button, nil schedule.
		map[string]any{"name": "manual", "script": "print('{}')"},
	})

	if len(acts) != 4 {
		t.Fatalf("want 4 actions, got %d", len(acts))
	}

	// 0: floored to the minimum, idle days preserved.
	if s := acts[0].Schedule; s == nil {
		t.Fatal("refresh: expected a schedule")
	} else if s.IntervalSeconds != MinAppScheduleSeconds {
		t.Errorf("refresh: interval = %d, want floored to %d", s.IntervalSeconds, MinAppScheduleSeconds)
	} else if s.MaxIdleDays != 7 {
		t.Errorf("refresh: max_idle_days = %d, want 7", s.MaxIdleDays)
	}

	// 1: cron wins, interval cleared.
	if s := acts[1].Schedule; s == nil {
		t.Fatal("daily: expected a schedule")
	} else if s.Cron != "MON 09:00" || s.IntervalSeconds != 0 {
		t.Errorf("daily: cron=%q interval=%d, want cron kept + interval cleared", s.Cron, s.IntervalSeconds)
	}

	// 2: no cadence → nil schedule.
	if acts[2].Schedule != nil {
		t.Errorf("empty_sched: want nil schedule, got %+v", acts[2].Schedule)
	}

	// 3: manual button.
	if acts[3].Schedule != nil {
		t.Errorf("manual: want nil schedule, got %+v", acts[3].Schedule)
	}

	// Notes should flag the floor, the clash, and the no-op schedule.
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"below the", "using cron", "will NOT self-update"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q; got:\n%s", want, joined)
		}
	}
}
