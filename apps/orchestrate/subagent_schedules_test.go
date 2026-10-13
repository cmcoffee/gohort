package orchestrate

// A sub-agent has no way in of its own: a schedule made for one wakes its
// top-level parent, which hands the work on, the way a channel bound to one is
// its parent's.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func subAgentScheduleSetup(t *testing.T, owner string) Database {
	t.Helper()
	root := parkedTestSetup(t)
	prev := ScheduleAgentFor
	ScheduleAgentFor = scheduleAgentFor
	t.Cleanup(func() { ScheduleAgentFor = prev })
	udb := UserDB(root, owner)
	for _, a := range []AgentRecord{
		{ID: "p", Name: "Writer", Owner: owner, OrchestratorPrompt: "p"},
		{ID: "s", Name: "Scout", Owner: owner, OwnedBy: "p", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAScheduleMadeForASubAgentWakesItsParent(t *testing.T) {
	subAgentScheduleSetup(t, "w")

	SaveStandingAgent(RootDB, StandingAgent{Owner: "w", Name: "sweep", AgentID: "s", Mission: "Check the feeds.", IntervalSeconds: 3600})
	sa, _ := GetStandingAgent(RootDB, "w", "sweep")
	if sa.AgentID != "p" || subAgentHandoffOf(sa.Mission) != "Scout" || !strings.HasPrefix(sa.Mission, "Check the feeds.") {
		t.Errorf("scheduled agent: runs %q, mission %q", sa.AgentID, sa.Mission)
	}
	// Saving it again does not add the line twice.
	SaveStandingAgent(RootDB, sa)
	if again, _ := GetStandingAgent(RootDB, "w", "sweep"); strings.Count(again.Mission, subAgentHandoffLead) != 1 {
		t.Errorf("the handoff line was added twice: %q", again.Mission)
	}

	SaveEventMonitor(RootDB, EventMonitor{Owner: "w", Name: "watch", Kind: "webhook", WakeAgent: "s"})
	if m, _ := GetEventMonitor(RootDB, "w", "watch"); m.WakeAgent != "p" || subAgentHandoffOf(m.WakeBrief) != "Scout" {
		t.Errorf("monitor: wakes %q, brief %q", m.WakeAgent, m.WakeBrief)
	}

	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s1", AgentID: "s", Username: "w",
		Prompt: "Gather the news", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatal(err)
	}
	rt := listAgentRecurringTasks("w", "")[0]
	if rt.Payload.AgentID != "p" || rt.Payload.SessionID != cortexSessionID("p") || recurringName(rt.Payload) != "Gather the news" {
		t.Errorf("recurring task: runs %q in %q, named %q", rt.Payload.AgentID, rt.Payload.SessionID, recurringName(rt.Payload))
	}

	// The Scheduler says whose work each one is.
	for _, row := range consoleAgentRows("w", UserDB(RootDB, "w"), "p") {
		if row.Name == "sweep" && row.Runs != "for sub-agent Scout" {
			t.Errorf("scheduled agent row runs = %q", row.Runs)
		}
	}
	if rows := consoleRecurringRows("w", "p"); len(rows) != 1 || rows[0].Runs != "for sub-agent Scout" || rows[0].Name != "Gather the news" {
		t.Errorf("recurring rows = %+v", rows)
	}
}

// Schedules made for a sub-agent before this was the rule move to its parent
// at start-up; a top-level agent's are left alone.
func TestOldSubAgentSchedulesMoveToTheParent(t *testing.T) {
	subAgentScheduleSetup(t, "v")
	ScheduleAgentFor = nil
	SaveStandingAgent(RootDB, StandingAgent{Owner: "v", Name: "old", AgentID: "s", Mission: "m", IntervalSeconds: 3600})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "v", Name: "own", AgentID: "p", Mission: "m", IntervalSeconds: 3600})
	// A recurring task armed for the sub-agent directly, as one made before
	// the rule was: made for the parent, then re-armed naming the sub-agent.
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s1", AgentID: "p", Username: "v",
		Prompt: "Gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatal(err)
	}
	rt := listAgentRecurringTasks("v", "")[0]
	UnscheduleTask(rt.TaskID)
	old := rt.Payload
	old.AgentID = "s"
	if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, old, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ScheduleAgentFor = scheduleAgentFor
	uid := recurringTaskUID(old)

	if n := foldSubAgentSchedules("v", ""); n != 2 {
		t.Errorf("folded %d, want the scheduled agent and the recurring task", n)
	}
	if sa, _ := GetStandingAgent(RootDB, "v", "old"); sa.AgentID != "p" {
		t.Errorf("old schedule still wakes %q", sa.AgentID)
	}
	if sa, _ := GetStandingAgent(RootDB, "v", "own"); sa.Mission != "m" {
		t.Errorf("the parent's own schedule changed: %q", sa.Mission)
	}
	rts := listAgentRecurringTasks("v", "")
	if len(rts) != 1 || rts[0].Payload.AgentID != "p" || recurringTaskUID(rts[0].Payload) != uid {
		t.Errorf("recurring task after the move: %+v (uid kept: %v)", rts, len(rts) == 1 && recurringTaskUID(rts[0].Payload) == uid)
	}
	if n := foldSubAgentSchedules("v", ""); n != 0 {
		t.Errorf("a second pass moved %d more", n)
	}
}
