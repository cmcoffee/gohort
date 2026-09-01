package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

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
