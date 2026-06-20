package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// memDB returns an in-memory Database for tests.
func memDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

// TestRunLedgerRoundTrip: a recorded run comes back whole via GetRun
// (raw rehydrated), while ListRuns returns it WITHOUT raw — the feed
// surface must never carry the sensitive field.
func TestRunLedgerRoundTrip(t *testing.T) {
	db := memDB(t)
	rec := RecordRun(db, RunRecord{
		Owner:   "alice",
		Agent:   "backup",
		Trigger: "schedule",
		Brief:   "nightly backup",
		Status:  RunOK,
		Summary: "backed up 3 hosts",
		Raw:     "host db-1 ok\nhost db-2 ok\nsecret-token=shouldNotLeak",
	})
	if rec.ID == "" {
		t.Fatal("RecordRun should assign an ID")
	}
	if rec.Started.IsZero() {
		t.Fatal("RecordRun should stamp Started")
	}

	got, ok := GetRun(db, "alice", rec.ID)
	if !ok {
		t.Fatal("GetRun should find the recorded run")
	}
	if got.Summary != "backed up 3 hosts" || got.Status != RunOK {
		t.Fatalf("metadata not preserved: %+v", got)
	}
	if got.Raw == "" {
		t.Fatal("GetRun should rehydrate Raw from the encrypted side table")
	}

	list := ListRuns(db, "alice", RunFilter{})
	if len(list) != 1 {
		t.Fatalf("expected 1 run, got %d", len(list))
	}
	if list[0].Raw != "" {
		t.Fatalf("ListRuns must NOT carry Raw (leak risk): %q", list[0].Raw)
	}
}

// TestRunLedgerOwnerIsolation: one owner's runs are invisible to another,
// both in ListRuns and GetRun. This is the cross-user leakage guard.
func TestRunLedgerOwnerIsolation(t *testing.T) {
	db := memDB(t)
	a := RecordRun(db, RunRecord{Owner: "alice", Agent: "x", Status: RunOK})
	_ = RecordRun(db, RunRecord{Owner: "bob", Agent: "y", Status: RunOK})

	if got := ListRuns(db, "alice", RunFilter{}); len(got) != 1 || got[0].Owner != "alice" {
		t.Fatalf("alice should see only her run, got %+v", got)
	}
	if got := ListRuns(db, "bob", RunFilter{}); len(got) != 1 || got[0].Owner != "bob" {
		t.Fatalf("bob should see only his run, got %+v", got)
	}
	// Bob cannot resolve Alice's run id under his own scope.
	if _, ok := GetRun(db, "bob", a.ID); ok {
		t.Fatal("GetRun must be owner-scoped — bob resolved alice's run")
	}
}

// TestRunLedgerFilterAndSort: filters narrow by agent/status, and results
// come back newest-first.
func TestRunLedgerFilterAndSort(t *testing.T) {
	db := memDB(t)
	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	RecordRun(db, RunRecord{Owner: "alice", Agent: "backup", Status: RunOK, Started: base})
	RecordRun(db, RunRecord{Owner: "alice", Agent: "patch", Status: RunFailed, Started: base.Add(time.Hour)})
	RecordRun(db, RunRecord{Owner: "alice", Agent: "backup", Status: RunAttention, Started: base.Add(2 * time.Hour)})

	all := ListRuns(db, "alice", RunFilter{})
	if len(all) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(all))
	}
	if !all[0].Started.After(all[1].Started) || !all[1].Started.After(all[2].Started) {
		t.Fatal("ListRuns should be newest-first")
	}

	byAgent := ListRuns(db, "alice", RunFilter{Agent: "backup"})
	if len(byAgent) != 2 {
		t.Fatalf("expected 2 backup runs, got %d", len(byAgent))
	}
	byStatus := ListRuns(db, "alice", RunFilter{Status: RunFailed})
	if len(byStatus) != 1 || byStatus[0].Agent != "patch" {
		t.Fatalf("expected 1 failed run (patch), got %+v", byStatus)
	}
	if got := ListRuns(db, "alice", RunFilter{Limit: 1}); len(got) != 1 {
		t.Fatalf("Limit should cap results, got %d", len(got))
	}
}

// TestRunLedgerPrune: recording past the per-owner cap drops the oldest,
// and the dropped run's raw goes with it.
func TestRunLedgerPrune(t *testing.T) {
	db := memDB(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var oldest RunRecord
	for i := 0; i < maxRunsPerOwner()+5; i++ {
		r := RecordRun(db, RunRecord{
			Owner:   "alice",
			Agent:   "loop",
			Status:  RunOK,
			Raw:     "output",
			Started: base.Add(time.Duration(i) * time.Minute),
		})
		if i == 0 {
			oldest = r
		}
	}
	if n := len(ListRuns(db, "alice", RunFilter{})); n != maxRunsPerOwner() {
		t.Fatalf("expected ledger capped at %d, got %d", maxRunsPerOwner(), n)
	}
	if _, ok := GetRun(db, "alice", oldest.ID); ok {
		t.Fatal("oldest run should have been pruned")
	}
}
