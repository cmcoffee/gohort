package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestMarkEventMonitorBroken_KeepsRecordAndPauses verifies the "mark, don't
// delete" safety: a monitor whose dependency is gone is flagged broken, paused,
// and unscheduled — but the record SURVIVES. Clearing the flag (a relink)
// leaves it PAUSED, so recovery is always an explicit owner Resume, never an
// automatic restart.
func TestMarkEventMonitorBroken_KeepsRecordAndPauses(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}

	SaveEventMonitor(db, EventMonitor{
		Owner: "u1", Name: "watch1", Kind: EventKindWatch,
		WakeAgent: "agentX", SchedulerID: "sched-1",
	})

	if !MarkEventMonitorBroken(db, "u1", "watch1", "wakes deleted agent \"X\"") {
		t.Fatal("MarkEventMonitorBroken returned false for an existing monitor")
	}
	m, ok := GetEventMonitor(db, "u1", "watch1")
	if !ok {
		t.Fatal("monitor was removed — it must be KEPT, not deleted")
	}
	if !m.Broken || m.BrokenReason == "" {
		t.Errorf("expected Broken with a reason, got Broken=%v reason=%q", m.Broken, m.BrokenReason)
	}
	if !m.Paused {
		t.Error("a broken monitor must be auto-paused")
	}
	if m.SchedulerID != "" {
		t.Errorf("broken monitor should be unscheduled, SchedulerID=%q", m.SchedulerID)
	}

	// Relink/heal: broken clears, but it MUST stay paused (resume is explicit).
	if !ClearEventMonitorBroken(db, "u1", "watch1") {
		t.Fatal("ClearEventMonitorBroken returned false")
	}
	m, _ = GetEventMonitor(db, "u1", "watch1")
	if m.Broken || m.BrokenReason != "" {
		t.Errorf("clear should drop broken+reason, got Broken=%v reason=%q", m.Broken, m.BrokenReason)
	}
	if !m.Paused {
		t.Error("clearing broken must LEAVE the monitor paused — resume is an explicit owner action")
	}
}

// TestMarkStandingAgentBroken_KeepsRecordAndPauses mirrors the monitor case for
// standing agents.
func TestMarkStandingAgentBroken_KeepsRecordAndPauses(t *testing.T) {
	withSchedulerDB(t)
	db := &DBase{Store: kvlite.MemStore()}

	SaveStandingAgent(db, StandingAgent{
		Owner: "u1", Name: "nightly", AgentID: "agentX", SchedulerID: "sched-2",
	})

	if !MarkStandingAgentBroken(db, "u1", "nightly", "runs deleted agent \"X\"") {
		t.Fatal("MarkStandingAgentBroken returned false for an existing standing agent")
	}
	sa, ok := GetStandingAgent(db, "u1", "nightly")
	if !ok {
		t.Fatal("standing agent was removed — it must be KEPT, not deleted")
	}
	if !sa.Broken || sa.BrokenReason == "" || !sa.Paused || sa.SchedulerID != "" {
		t.Errorf("expected broken+paused+unscheduled, got %+v", sa)
	}

	if !ClearStandingAgentBroken(db, "u1", "nightly") {
		t.Fatal("ClearStandingAgentBroken returned false")
	}
	sa, _ = GetStandingAgent(db, "u1", "nightly")
	if sa.Broken || sa.BrokenReason != "" {
		t.Errorf("clear should drop broken+reason, got Broken=%v reason=%q", sa.Broken, sa.BrokenReason)
	}
	if !sa.Paused {
		t.Error("clearing broken must LEAVE the standing agent paused — resume is explicit")
	}
}
