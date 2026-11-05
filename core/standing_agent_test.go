package core

import (
	"context"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
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

// TestRunDelegationCarriesDispatchChain: a delegation hands the delegating
// agent's id to the runner (as StandingAgent.DispatchedBy), which is what lets
// the delegate reach the channels its delegator reaches instead of handing its
// output back up to be relayed. Blank ids are dropped so an unset caller never
// becomes an empty seed in the channel-scope walk.
func TestRunDelegationCarriesDispatchChain(t *testing.T) {
	db := memDB(t)
	var seen StandingAgent
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		seen = sa
		return StandingRunResult{Status: RunOK, Summary: "done"}
	})
	defer RegisterStandingRunner(nil)

	RunDelegation(context.Background(), db, "alice", "research", "dig into X", "chat", "", "  ")
	if len(seen.DispatchedBy) != 1 || seen.DispatchedBy[0] != "chat" {
		t.Fatalf("DispatchedBy = %v, want [chat] (blanks dropped)", seen.DispatchedBy)
	}
	if seen.Mission != "dig into X" || seen.AgentID != "research" {
		t.Fatalf("delegation record wrong: %+v", seen)
	}

	// A delegation with no known delegator runs with its own scope — no empty
	// seed, which would otherwise widen the walk.
	RunDelegation(context.Background(), db, "alice", "research", "dig into X")
	if len(seen.DispatchedBy) != 0 {
		t.Fatalf("undelegated run should carry no chain, got %v", seen.DispatchedBy)
	}
}

// TestScheduledRunCarriesNoDispatchChain: a stored schedule has no dispatching
// parent, so it must never pick up channel reach it wasn't given. Guards the
// deliberate split from ReportAgentID, which every schedule has.
func TestScheduledRunCarriesNoDispatchChain(t *testing.T) {
	db := memDB(t)
	var seen StandingAgent
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		seen = sa
		return StandingRunResult{Status: RunOK}
	})
	defer RegisterStandingRunner(nil)

	executeStandingRun(context.Background(), db, StandingAgent{
		Owner: "alice", Name: "nightly", AgentID: "worker", Mission: "run",
		ReportAgentID: "chat", ReportSessionID: "s1",
	}, "schedule")
	if len(seen.DispatchedBy) != 0 {
		t.Fatalf("a schedule must carry no dispatch chain, got %v", seen.DispatchedBy)
	}
}

// TestClearStandingAgentBrokenRestartsTheObjectiveAllowance: a stalled
// objective is parked with its reason, and the owner fixes whatever the reason
// named and resumes. If the allowance carried its old count, the resumed
// schedule would stall again on its very first run and hand back the same
// refusal — the defect the recurring path measured its attempts against a
// fresh baseline to avoid, reached here by resetting the counter instead
// (a standing agent has no lifetime fire count to subtract from).
//
// The attempt HISTORY deliberately survives: the next run still gets told what
// the earlier ones tried.
func TestClearStandingAgentBrokenRestartsTheObjectiveAllowance(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "nightly", AgentID: "ag",
		Until: "the report is filed", MaxAttempts: 3, UnmetCount: 3,
		Attempts:     []ObjectiveAttempt{{At: "2026-09-07T04:00:00Z", Reason: "the API refused"}},
		Broken:       true,
		BrokenReason: "objective not met after 3 attempt(s) — the API refused",
		Paused:       true,
	})

	if !ClearStandingAgentBroken(db, "u", "nightly") {
		t.Fatal("the parked schedule could not be resumed")
	}
	got, ok := GetStandingAgent(db, "u", "nightly")
	if !ok {
		t.Fatal("the schedule vanished on resume")
	}
	if got.UnmetCount != 0 {
		t.Errorf("resumed with %d attempts already spent — it would stall on its first run", got.UnmetCount)
	}
	if len(got.Attempts) != 1 || got.Attempts[0].Reason != "the API refused" {
		t.Errorf("the attempt history did not survive the resume: %+v", got.Attempts)
	}
	if got.Broken || got.BrokenReason != "" {
		t.Error("the park was not cleared")
	}
	if !got.Paused {
		t.Error("Paused must stay: resuming a parked schedule is a separate, explicit action")
	}
}

// Slice 2 of docs/design/scheduling-unification.md. Three records carried the same
// "stopped and kept" state and each read it back itself; they read it through
// one pair of functions now.
func TestParkCauseOfReadsEveryRecordTheSameWay(t *testing.T) {
	if got := ParkCauseOf(false, ""); got != "" {
		t.Errorf("a running schedule is not parked: %q", got)
	}
	if got := ParkCauseOf(false, ParkedByObjective); got != "" {
		t.Errorf("a stale cause on a running schedule means nothing: %q", got)
	}
	if got := ParkCauseOf(true, ParkedByObjective); got != ParkedByObjective {
		t.Errorf("a stored cause wins: %q", got)
	}
	// Records written before the cause existed carry none, and the only thing
	// that used to park one was a missing dependency.
	if got := ParkCauseOf(true, ""); got != ParkedByDependency {
		t.Errorf("an old parked record reads as a dependency: %q", got)
	}
	if got := ParkCauseOf(true, "   "); got != ParkedByDependency {
		t.Errorf("blank is not a cause: %q", got)
	}
}

// Relink is the repair for exactly one park. A stalled objective is parked too,
// and offering it a picker of live agents describes a problem it does not have.
func TestRelinkFixesOnlyAMissingDependency(t *testing.T) {
	if !RelinkFixesIt(ParkedByDependency) {
		t.Error("a schedule whose target is gone is what Relink is for")
	}
	for _, other := range []string{ParkedByObjective, StoppedByOwner, StoppedByMet, ""} {
		if RelinkFixesIt(other) {
			t.Errorf("%q is not repaired by re-pointing anything", other)
		}
	}
}

// The standing reader is now that shared one, so the two cannot drift.
func TestStandingParkCauseIsTheSharedReader(t *testing.T) {
	sa := StandingAgent{Broken: true, BrokenCause: ParkedByObjective}
	if StandingParkCause(sa) != ParkCauseOf(sa.Broken, sa.BrokenCause) {
		t.Error("the standing reader has grown a rule of its own again")
	}
	if StandingParkCause(StandingAgent{}) != "" {
		t.Error("a running agent reads as unparked")
	}
}

// A random cadence draws each gap between the two bounds, and says so.
// Observed: "every 30-60 minutes at random" was flattened to a fixed 45,
// because a scheduled agent could only hold one interval.
func TestStandingRandomGaps(t *testing.T) {
	sa := StandingAgent{Name: "poster", IntervalSeconds: 1800, MaxIntervalSeconds: 3600}
	from := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		next, err := nextStandingRun(sa, from)
		if err != nil {
			t.Fatal(err)
		}
		gap := next.Sub(from)
		if gap < 30*time.Minute || gap > 60*time.Minute {
			t.Fatalf("gap %s outside 30-60 minutes", gap)
		}
		seen[gap] = true
	}
	if len(seen) < 2 {
		t.Fatalf("every gap was the same: not random")
	}
	if got := StandingScheduleLabel(sa); got != "at random, every 30-60 minutes" {
		t.Errorf("label %q", got)
	}
	// A bound not above the interval is the plain fixed interval.
	sa.MaxIntervalSeconds = 1800
	if next, _ := nextStandingRun(sa, from); next.Sub(from) != 30*time.Minute {
		t.Errorf("fixed interval drifted: %s", next.Sub(from))
	}
	if got := StandingScheduleLabel(sa); got != "every 30 minutes" {
		t.Errorf("label %q", got)
	}
}

// A random cadence kept to a daily window (the old fields, read through
// EffectiveTiming): a run that would land after it closes waits for the next
// day's opening, one inside it stands, and the label says so.
func TestStandingWindow(t *testing.T) {
	loc := time.UTC
	sa := StandingAgent{Name: "poster", IntervalSeconds: 1800, MaxIntervalSeconds: 3600, HasWindow: true, WindowFromMin: 9 * 60, WindowToMin: 17 * 60}
	late := time.Date(2026, 10, 12, 16, 50, 0, 0, loc)
	next, err := nextStandingRun(sa, late)
	if err != nil {
		t.Fatal(err)
	}
	// Early in the next day's window, spread across the first gap rather than
	// on its first minute every morning.
	if open := time.Date(2026, 10, 13, 9, 0, 0, 0, loc); next.Before(open) || next.After(open.Add(30*time.Minute)) {
		t.Fatalf("a run past the window went to %s, want early on the 13th's window", next)
	}
	mid := time.Date(2026, 10, 12, 11, 0, 0, 0, loc)
	if next, _ := nextStandingRun(sa, mid); next.Before(mid.Add(30*time.Minute)) || next.After(mid.Add(60*time.Minute)) {
		t.Fatalf("a run inside the window moved: %s", next)
	}
	early := time.Date(2026, 10, 12, 6, 0, 0, 0, loc)
	if next, _ := nextStandingRun(sa, early); next.Before(time.Date(2026, 10, 12, 9, 0, 0, 0, loc)) || next.After(time.Date(2026, 10, 12, 9, 30, 0, 0, loc)) {
		t.Fatalf("a run before the window did not wait for it: %s", next)
	}
	if got := StandingScheduleLabel(sa); got != "at random, every 30-60 minutes between 09:00 and 17:00" {
		t.Errorf("label %q", got)
	}
}

// Every old "{days} HH:MM" form fires at exactly the times it always did now
// that scheduled agents go through core/timing: same answer as the old parser
// from every hour of a week.
func TestCronFiresTheSameThroughTiming(t *testing.T) {
	for _, spec := range []string{"daily 09:00", "weekdays 17:00", "weekends 10:30", "FRI 21:30", "MON,WED,FRI 08:00", "everyday 00:00"} {
		sa := StandingAgent{Name: "x", Cron: spec}
		for h := 0; h < 7*24; h++ {
			from := time.Date(2026, 10, 12, 0, 13, 0, 0, time.UTC).Add(time.Duration(h) * time.Hour)
			want, err := NextCronOccurrence(spec, from)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextStandingRun(sa, from)
			if err != nil || !got.Equal(want) {
				t.Fatalf("%s from %s: got %s, want %s (%v)", spec, from, got, want, err)
			}
		}
	}
	// An annual date keeps its old form and still fires.
	if _, err := nextStandingRun(StandingAgent{Name: "x", Cron: "DEC-25 09:00"}, time.Now()); err != nil {
		t.Fatalf("annual date: %v", err)
	}
}

// withSchedulerDB points the package scheduler DB at an in-memory store for
// the duration of a test, so ScheduleTask / ListScheduledTasks / re-arm work.
func withSchedulerDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	schedDBMu.Lock()
	prev := schedDB
	schedDB = db
	schedDBMu.Unlock()
	t.Cleanup(func() {
		schedDBMu.Lock()
		schedDB = prev
		schedDBMu.Unlock()
	})
	return db
}

// TestRearmStrandedStandingAgents: an active agent whose NextRun is frozen in
// the past with no live task gets rescheduled to a FUTURE time; paused agents,
// agents with a live task, and agents whose next fire is imminent are left
// alone.
func TestRearmStrandedStandingAgents(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	now := time.Now()

	// Stranded: active, NextRun 9 days ago, no scheduler task.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "wiwee", Cron: "daily 12:00",
		NextRun: now.Add(-9 * 24 * time.Hour), SchedulerID: "dead-task-id",
	})
	// Paused: must never be re-armed even though NextRun is stale.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "paused-job", Cron: "daily 09:00",
		NextRun: now.Add(-9 * 24 * time.Hour), Paused: true,
	})
	// Healthy: NextRun in the future — leave it.
	SaveStandingAgent(db, StandingAgent{
		Owner: "u", Name: "healthy", Cron: "daily 08:00",
		NextRun: now.Add(6 * time.Hour),
	})

	revived := RearmStrandedStandingAgents(db)
	if revived != 1 {
		t.Fatalf("expected exactly 1 revived, got %d", revived)
	}

	// The stranded one now points at a FUTURE fire and has a fresh task id.
	sa, _ := GetStandingAgent(db, "u", "wiwee")
	if !sa.NextRun.After(now) {
		t.Fatalf("stranded agent NextRun not advanced to the future: %s", sa.NextRun)
	}
	if sa.NextRun.Before(now) {
		t.Fatal("missed runs must not be backfilled — NextRun must be future")
	}
	if sa.SchedulerID == "dead-task-id" || sa.SchedulerID == "" {
		t.Fatalf("stranded agent should have a fresh scheduler id, got %q", sa.SchedulerID)
	}

	// Paused stayed paused and unscheduled.
	if p, _ := GetStandingAgent(db, "u", "paused-job"); p.SchedulerID != "" {
		t.Error("paused agent must not be re-armed")
	}

	// A second sweep is a no-op now that everything has a live task.
	if again := RearmStrandedStandingAgents(db); again != 0 {
		t.Fatalf("second sweep should revive nothing, got %d", again)
	}
}

// A standing agent's next occurrence does not exist while its fire runs: the
// re-arm is deferred and re-reads the record. So an attempt that asks to come
// back later just writes the time down, and arming has to honour it — for that
// one occurrence, and then go back to the cadence (docs/design/objective-pacing.md).
func TestScheduleStandingAgentHonoursAPacedAttempt(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	paced := time.Now().Add(5 * time.Hour).Truncate(time.Second)

	sa := StandingAgent{
		Owner: "u", Name: "release-notes", IntervalSeconds: 3600,
		Until:          "the notes are published",
		NextAttemptAt:  paced,
		NextAttemptWhy: "the build is still running",
	}
	if err := ScheduleStandingAgent(db, sa); err != nil {
		t.Fatalf("ScheduleStandingAgent: %v", err)
	}

	got, ok := GetStandingAgent(db, "u", "release-notes")
	if !ok {
		t.Fatal("the record vanished")
	}
	if !got.NextRun.Equal(paced) {
		t.Fatalf("armed for %s, but the attempt asked for %s", got.NextRun, paced)
	}
	if !got.NextAttemptAt.IsZero() {
		t.Error("the ask was not consumed, so it would move every future occurrence too")
	}
	if got.NextAttemptWhy != "the build is still running" {
		t.Error("the reason must outlive the ask by one occurrence — it explains the run now armed")
	}

	// The arming AFTER that one is back on the cadence, and the reason goes
	// with the occurrence it explained.
	if err := ScheduleStandingAgent(db, got); err != nil {
		t.Fatalf("second arm: %v", err)
	}
	got2, _ := GetStandingAgent(db, "u", "release-notes")
	if got2.NextRun.Equal(paced) {
		t.Error("the second arm reused the paced time; pacing moves ONE occurrence")
	}
	if diff := got2.NextRun.Sub(time.Now()); diff > 61*time.Minute || diff < 59*time.Minute {
		t.Errorf("second arm is %s out, wanted the hourly cadence", diff)
	}
	if got2.NextAttemptWhy != "" {
		t.Error("a stale reason would claim the cadence's own time was chosen by an attempt")
	}
}

// A time that has already passed is not a schedule. It can happen when a fire
// outlives the wait it asked for, and the answer is the cadence, not a fire in
// the past.
func TestAPacedTimeInThePastIsIgnored(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}

	sa := StandingAgent{
		Owner: "u", Name: "stale", IntervalSeconds: 600,
		NextAttemptAt:  time.Now().Add(-2 * time.Hour),
		NextAttemptWhy: "waiting on something that already happened",
	}
	if err := ScheduleStandingAgent(db, sa); err != nil {
		t.Fatalf("ScheduleStandingAgent: %v", err)
	}
	got, _ := GetStandingAgent(db, "u", "stale")
	if !got.NextRun.After(time.Now()) {
		t.Fatalf("armed in the past: %s", got.NextRun)
	}
	if !got.NextAttemptAt.IsZero() || got.NextAttemptWhy != "" {
		t.Error("a stale ask must be cleared, not left to be reconsidered every arm")
	}
}
