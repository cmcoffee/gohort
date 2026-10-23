package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

type fakeRecurring struct{ cancelled []map[string]any }

func (f *fakeRecurring) recurringSchedule(map[string]any) (string, error) { return "", nil }
func (f *fakeRecurring) recurringList(map[string]any) (string, error)     { return "", nil }
func (f *fakeRecurring) recurringMove(map[string]any) (string, error)     { return "", nil }
func (f *fakeRecurring) recurringTasks() []recurringTaskRow               { return nil }
func (f *fakeRecurring) recurringCancel(m map[string]any) (string, error) {
	f.cancelled = append(f.cancelled, m)
	return "CANCELLED ok.", nil
}

// Another agent's recurring task is reached by its id or its name, even after
// its agent was deleted; a name it shares with a standing agent is refused
// rather than taking the standing agent. A session rebuilt a deleted agent's
// jobs under the same names, then deleted by name and lost the new ones.
func TestScheduleReachesEveryRecurringTaskAndRefusesAClash(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	PreInitScheduler()
	t.Cleanup(func() { RootDB = saved })

	p := orchUpdatePayload{Username: "u", AgentID: "gone-agent", SessionID: "s1", Name: "Random engagement", Prompt: "engage", IntervalSeconds: 3600}
	taskID, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnscheduleTask(taskID) })
	SaveStandingAgent(RootDB, StandingAgent{Name: "Random engagement", Owner: "u", AgentID: "new-agent"})
	standingDeleted := false
	impl := map[string]ToolHandlerFunc{"delete_standing_agent": func(context.Context, map[string]any) (string, error) {
		standingDeleted = true
		return "deleted", nil
	}}
	ctx := context.Background()

	rec := &fakeRecurring{}
	_, err = scheduleControl(ctx, "u", "builder", impl, rec, "delete", "Random engagement", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), taskID) {
		t.Fatalf("a name held by a standing agent and a recurring task should be refused, naming the task: %v", err)
	}
	if standingDeleted || len(rec.cancelled) != 0 {
		t.Fatal("a refused clash deleted something")
	}

	if _, err := scheduleControl(ctx, "u", "builder", impl, rec, "delete", taskID, map[string]any{}); err != nil {
		t.Fatalf("delete by the task's id: %v", err)
	}
	if len(rec.cancelled) != 1 || rec.cancelled[0]["id"] != taskID || rec.cancelled[0]["agent"] != "all" {
		t.Errorf("cancelled %+v, want the task by id across agents", rec.cancelled)
	}

	rec = &fakeRecurring{}
	if _, err := scheduleControl(ctx, "u", "builder", impl, rec, "delete", "Random engagement", map[string]any{"agent": "gone-agent"}); err != nil {
		t.Fatalf("delete by name on its (deleted) agent: %v", err)
	}
	if len(rec.cancelled) != 1 || rec.cancelled[0]["id"] != taskID {
		t.Errorf("a name on the agent should resolve to the task's id: %+v", rec.cancelled)
	}

	// A recurring task pauses by its name on its agent, as every kind does.
	// Pausing re-arms it under a new id, so the cleanup takes every task u
	// has, not only the first.
	t.Cleanup(func() {
		for _, rt := range listAgentRecurringTasks("u", "") {
			UnscheduleTask(rt.TaskID)
		}
	})
	out, err := scheduleControl(ctx, "u", "builder", impl, rec, "pause", "Random engagement", map[string]any{"agent": "gone-agent"})
	if err != nil || !strings.HasPrefix(out, "Paused \"Random engagement\"") {
		t.Errorf("a recurring task pauses by its name on its agent: %q %v", out, err)
	}
}
