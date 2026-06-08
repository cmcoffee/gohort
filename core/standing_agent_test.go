package core

import (
	"context"
	"testing"
	"time"
)

func TestNextCronOccurrence(t *testing.T) {
	// A Wednesday noon reference point.
	from := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		spec    string
		wantErr bool
		check   func(time.Time) bool
	}{
		{"FRI 21:30", false, func(g time.Time) bool {
			return g.Weekday() == time.Friday && g.Hour() == 21 && g.Minute() == 30
		}},
		{"daily 08:00", false, func(g time.Time) bool {
			// Next 08:00 after Wed noon is Thursday 08:00.
			return g.Hour() == 8 && g.After(from)
		}},
		{"weekends 10:00", false, func(g time.Time) bool {
			return (g.Weekday() == time.Saturday || g.Weekday() == time.Sunday) && g.Hour() == 10
		}},
		{"APR-3 09:00", false, func(g time.Time) bool {
			return g.Month() == time.April && g.Day() == 3 && g.After(from)
		}},
		{"FRI", true, nil},         // missing time
		{"FRI 25:00", true, nil},   // bad hour
		{"BLERG 09:00", true, nil}, // bad day
	}
	for _, c := range cases {
		got, err := NextCronOccurrence(c.spec, from)
		if c.wantErr {
			if err == nil {
				t.Errorf("NextCronOccurrence(%q) expected error, got %v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NextCronOccurrence(%q) unexpected error: %v", c.spec, err)
			continue
		}
		if !c.check(got) {
			t.Errorf("NextCronOccurrence(%q) = %v, failed check", c.spec, got)
		}
	}
}

func TestStandingAgentCRUD(t *testing.T) {
	db := memDB(t)
	SaveStandingAgent(db, StandingAgent{Owner: "alice", Name: "backup", Cron: "daily 02:00"})
	SaveStandingAgent(db, StandingAgent{Owner: "alice", Name: "patch", Cron: "FRI 21:30"})
	SaveStandingAgent(db, StandingAgent{Owner: "bob", Name: "backup", Cron: "daily 03:00"})

	if got := ListStandingAgents(db, "alice"); len(got) != 2 {
		t.Fatalf("alice should have 2 standing agents, got %d", len(got))
	}
	if got := ListStandingAgents(db, "bob"); len(got) != 1 {
		t.Fatalf("bob should have 1, got %d", len(got))
	}
	if _, ok := GetStandingAgent(db, "alice", "backup"); !ok {
		t.Fatal("expected alice/backup to exist")
	}
	// Owner-scoped: bob's "backup" is a different record from alice's.
	if sa, _ := GetStandingAgent(db, "bob", "backup"); sa.Cron != "daily 03:00" {
		t.Fatalf("owner scoping broken: bob/backup cron = %q", sa.Cron)
	}

	DeleteStandingAgent(db, "alice", "patch")
	if got := ListStandingAgents(db, "alice"); len(got) != 1 {
		t.Fatalf("after delete, alice should have 1, got %d", len(got))
	}
}

// TestExecuteStandingRunNoRunner: with no runner registered, a fired run
// records a non-fatal "attention" entry rather than doing anything.
func TestExecuteStandingRunNoRunner(t *testing.T) {
	RegisterStandingRunner(nil)
	db := memDB(t)
	sa := StandingAgent{Owner: "alice", Name: "backup", Mission: "back up the fleet"}

	rec := executeStandingRun(context.Background(), db, sa, "manual")
	if rec.Status != RunAttention {
		t.Fatalf("expected attention status with no runner, got %q", rec.Status)
	}
	if got, ok := GetRun(db, "alice", rec.ID); !ok || got.Agent != "backup" || got.Trigger != "manual" {
		t.Fatalf("run not recorded to ledger: %+v ok=%v", got, ok)
	}
}

// TestExecuteStandingRunWithRunner: the registered runner's result is
// recorded — including raw rehydration — and a non-ok status fires the
// escalator.
func TestExecuteStandingRunWithRunner(t *testing.T) {
	db := memDB(t)

	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		return StandingRunResult{
			Status:  RunFailed,
			Summary: "host db-3 unreachable",
			Raw:     "ssh db-3: connection refused\ntoken=shouldNotLeakToFeed",
			Err:     "1 host down",
		}
	})
	defer RegisterStandingRunner(nil)

	var escalated *RunRecord
	RegisterStandingEscalator(func(r RunRecord) { escalated = &r })
	defer RegisterStandingEscalator(nil)

	sa := StandingAgent{Owner: "alice", Name: "patch", Mission: "patch servers"}
	rec := executeStandingRun(context.Background(), db, sa, "schedule")

	if rec.Status != RunFailed || rec.Summary != "host db-3 unreachable" {
		t.Fatalf("runner result not recorded: %+v", rec)
	}
	if escalated == nil || escalated.Status != RunFailed {
		t.Fatal("non-ok status should fire the escalator")
	}
	// Feed (ListRuns) must not carry raw; detail (GetRun) rehydrates it.
	list := ListRuns(db, "alice", RunFilter{})
	if len(list) != 1 || list[0].Raw != "" {
		t.Fatalf("ListRuns leaked raw or wrong count: %+v", list)
	}
	full, _ := GetRun(db, "alice", rec.ID)
	if full.Raw == "" {
		t.Fatal("GetRun should rehydrate the run's raw output")
	}
}
