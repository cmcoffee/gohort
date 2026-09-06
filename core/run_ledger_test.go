package core

import (
	"github.com/cmcoffee/snugforge/kvlite"
	"testing"
	"time"
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

// A recurring fire records the AGENT that ran, so the schedule's own name had
// nowhere to live and "did the Snuglab blog post task run today?" came back
// "No runs recorded yet." — the same sentence an empty ledger returns.
func TestRunLedgerFilterByTaskName(t *testing.T) {
	db := memDB(t)
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	RecordRun(db, RunRecord{Owner: "alice", Agent: "Moltbook agent", Task: "Snuglab blog post", Status: RunOK, Started: base})
	RecordRun(db, RunRecord{Owner: "alice", Agent: "Moltbook agent", Task: "morning digest", Status: RunOK, Started: base.Add(time.Hour)})
	RecordRun(db, RunRecord{Owner: "alice", Agent: "Moltbook agent", Status: RunOK, Started: base.Add(2 * time.Hour)})

	byTask := ListRuns(db, "alice", RunFilter{Task: "Snuglab blog post"})
	if len(byTask) != 1 {
		t.Fatalf("expected the one Snuglab fire, got %d", len(byTask))
	}
	// The agent label still reaches every run it ran, tasks included: adding an
	// axis must not narrow the one that was already there.
	if got := ListRuns(db, "alice", RunFilter{Agent: "Moltbook agent"}); len(got) != 3 {
		t.Fatalf("agent filter should still return all 3, got %d", len(got))
	}
	// A task name is not an agent name. This is the query that used to look
	// like proof a schedule never fired.
	if got := ListRuns(db, "alice", RunFilter{Agent: "Snuglab blog post"}); len(got) != 0 {
		t.Fatalf("task name should not match the agent axis, got %d", len(got))
	}
	// Task ANDs with the other axes rather than replacing them.
	if got := ListRuns(db, "alice", RunFilter{Task: "Snuglab blog post", Status: RunFailed}); len(got) != 0 {
		t.Fatalf("task+status should AND, got %d", len(got))
	}
}

// A run with no Task is a standing agent, a monitor or a trigger, whose Agent
// already IS its name. Those must stay reachable exactly as before.
func TestTaskFilterLeavesLegacyRunsAlone(t *testing.T) {
	db := memDB(t)
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	RecordRun(db, RunRecord{Owner: "alice", Agent: "nightly-backup", Status: RunOK, Started: base})

	if got := ListRuns(db, "alice", RunFilter{Agent: "nightly-backup"}); len(got) != 1 {
		t.Fatalf("a task-less run should still match its agent, got %d", len(got))
	}
	if got := ListRuns(db, "alice", RunFilter{Task: "nightly-backup"}); len(got) != 0 {
		t.Fatalf("a task-less run has no task to match, got %d", len(got))
	}
}

// The bug this closes: RunRecord.Agent is a display label written by four kinds
// of caller into one flat string, so a standing schedule named "daily-stories"
// and an agent named "daily-stories" were one key. Asking for either one's
// history returned both, silently, on a card that looked authoritative.
func TestSubjectSeparatesThingsThatShareADisplayName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	RecordRun(db, RunRecord{Owner: "u", Agent: "daily-stories", Status: RunOK, Summary: "the schedule"}.AboutStanding("daily-stories"))
	RecordRun(db, RunRecord{Owner: "u", Agent: "daily-stories", Status: RunFailed, Summary: "the agent"}.AboutAgent("ag-news"))

	sched := ListRuns(db, "u", RunFilter{}.AboutStanding("daily-stories"))
	if len(sched) != 1 || sched[0].Summary != "the schedule" {
		t.Fatalf("the schedule's history must be its own: %+v", sched)
	}
	agent := ListRuns(db, "u", RunFilter{}.AboutAgent("ag-news", ""))
	if len(agent) != 1 || agent[0].Summary != "the agent" {
		t.Fatalf("the agent's history must be its own: %+v", agent)
	}
	// The old way still sees both — which is precisely why it was wrong.
	if both := ListRuns(db, "u", RunFilter{Agent: "daily-stories"}); len(both) != 2 {
		t.Errorf("a label query is a label query; got %d", len(both))
	}
}

// Kinds cannot collide even when a user names two things identically, because
// the namespace is part of the identity.
func TestSubjectsAreNamespacedByKind(t *testing.T) {
	name := "nightly"
	subs := map[string]bool{
		(RunRecord{}).AboutStanding(name).Subject: true,
		(RunRecord{}).AboutMonitor(name).Subject:  true,
		(RunRecord{}).AboutTrigger(name).Subject:  true,
		(RunRecord{}).AboutAgent(name).Subject:    true,
	}
	if len(subs) != 4 {
		t.Errorf("four kinds sharing one name must be four subjects, got %d: %v", len(subs), subs)
	}
	if (RunRecord{}).AboutAgent("").Subject != "" || (RunRecord{}).AboutStanding("   ").Subject != "" {
		t.Error("an empty id is no identity at all — it must not become the bare kind prefix, which would match every unnamed thing")
	}
}

// A ledger that forgets everything older than the fix is worse than one that
// keeps answering the old way for old rows.
func TestLegacyRunsStayReachable(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	RecordRun(db, RunRecord{Owner: "u", Agent: "backup", Status: RunOK, Summary: "before subjects"})
	RecordRun(db, RunRecord{Owner: "u", Agent: "backup", Status: RunOK, Summary: "after"}.AboutStanding("backup"))

	got := ListRuns(db, "u", RunFilter{}.AboutStanding("backup"))
	if len(got) != 2 {
		t.Fatalf("a subject query with a label fallback must still find the old rows; got %d", len(got))
	}

	// Without a label to fall back to, a subject query must NOT sweep up every
	// subject-less row — that would hand one thing the whole ledger's history.
	only := ListRuns(db, "u", RunFilter{Subject: (RunRecord{}).AboutStanding("backup").Subject})
	if len(only) != 1 || only[0].Summary != "after" {
		t.Errorf("a subject-only query must not widen to unrelated legacy rows: %+v", only)
	}
}
