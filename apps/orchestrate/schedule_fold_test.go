package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// recordingRecurring stands in for a chatTurn's recurring path and records
// what the fold hands it.
type recordingRecurring struct {
	scheduled []map[string]any
	cancelled []map[string]any
	moved     []map[string]any
	tasks     []recurringTaskRow
}

func (r *recordingRecurring) recurringSchedule(args map[string]any) (string, error) {
	r.scheduled = append(r.scheduled, args)
	return "SCHEDULED_OK id=t1", nil
}
func (r *recordingRecurring) recurringList(args map[string]any) (string, error) {
	return `{"tasks":[{"id":"t1","name":"build watch"}]}`, nil
}
func (r *recordingRecurring) recurringCancel(args map[string]any) (string, error) {
	r.cancelled = append(r.cancelled, args)
	return "CANCELLED ok", nil
}
func (r *recordingRecurring) recurringMove(args map[string]any) (string, error) {
	r.moved = append(r.moved, args)
	return "MOVED ok", nil
}
func (r *recordingRecurring) recurringTasks() []recurringTaskRow { return r.tasks }

// A plain agent (no fleet) sees schedule with at and every only, and its
// words become the recurring path's pattern: no interval_minutes, no
// pattern, no max_fires chosen by the agent.
func TestPlainAgentScheduleIsItsOwnRecurringPath(t *testing.T) {
	rec := &recordingRecurring{}
	tool := scheduleToolDef(nil, "craig", "a1", nil, rec)
	when := tool.Tool.Parameters["when"]
	if len(when.Enum) != 2 || when.Enum[0] != "at" || when.Enum[1] != "every" {
		t.Errorf("a plain agent is offered %v; it can only keep its own clock", when.Enum)
	}
	for _, p := range []string{"url", "tool_name", "check", "pipeline", "machine"} {
		if _, ok := tool.Tool.Parameters[p]; ok {
			t.Errorf("a plain agent is offered %q, which it cannot use", p)
		}
	}
	for _, p := range []string{"agent", "to", "random", "until", "max_attempts", "stop_after"} {
		if _, ok := tool.Tool.Parameters[p]; !ok {
			t.Errorf("a plain agent lost %q", p)
		}
	}
	if strings.Contains(tool.Tool.Description, "value_crosses") {
		t.Error("the description offers a monitor the agent cannot make")
	}
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }

	if _, err := call(map[string]any{"when": "every", "time": "every 15 minutes", "then": "check the build, post if red", "name": "build watch", "stop_after": 5}); err != nil {
		t.Fatal(err)
	}
	got := rec.scheduled[0]
	if got["pattern"] != RecurringFixed || got["interval_minutes"] != 15 || got["prompt"] != "check the build, post if red" || got["name"] != "build watch" || got["max_fires"] != 5 {
		t.Errorf("fixed cadence translated wrong: %v", got)
	}
	if _, err := call(map[string]any{"when": "every", "time": "every morning at 8", "then": "summarize overnight alerts", "to": "cortex", "until": "the alerts are triaged", "max_attempts": 3}); err != nil {
		t.Fatal(err)
	}
	got = rec.scheduled[1]
	if got["pattern"] != RecurringDaily || got["daily_at"] != "08:00" || got["to"] != "cortex" || got["until"] != "the alerts are triaged" || got["max_attempts"] != 3 {
		t.Errorf("daily cadence translated wrong: %v", got)
	}
	if _, err := call(map[string]any{"when": "every", "time": "90s", "then": "x", "random": true, "times_per_day": 4, "active_from": "09:00", "active_to": "17:00", "min_gap_minutes": 30}); err != nil {
		t.Fatal(err)
	}
	got = rec.scheduled[2]
	if got["pattern"] != RecurringRandom || got["times_per_day"] != 4 || got["active_from"] != "09:00" || got["min_gap_minutes"] != 30 {
		t.Errorf("random cadence translated wrong: %v", got)
	}
	// A weekday pattern is beyond a task of its own, and the agent is told so
	// rather than given a daily task it did not ask for.
	if _, err := call(map[string]any{"when": "every", "time": "weekdays 17:00", "then": "x"}); err == nil || !strings.Contains(err.Error(), "weekday") {
		t.Errorf("a weekday pattern was not refused plainly: %v", err)
	}
	// at = a task that fires once after the delay.
	if _, err := call(map[string]any{"when": "at", "time": "in 20 minutes", "then": "tell the user it's time"}); err != nil {
		t.Fatal(err)
	}
	got = rec.scheduled[3]
	if got["max_fires"] != 1 || got["interval_minutes"] != 20 || got["prompt"] != "tell the user it's time" {
		t.Errorf("one-shot translated wrong: %v", got)
	}
	// A monitor is not on offer, and the refusal says what is.
	if _, err := call(map[string]any{"when": "value_crosses", "url": "https://x", "compare_op": "<", "threshold": "1", "then": "x"}); err == nil || !strings.Contains(err.Error(), "only its own work") {
		t.Errorf("a monitor on a plain agent was not refused plainly: %v", err)
	}

	// list / delete / move reach the agent's own tasks by NAME, not id.
	rec.tasks = []recurringTaskRow{{TaskID: "t1", Payload: orchUpdatePayload{Name: "build watch"}}}
	out, err := call(map[string]any{"action": "list"})
	if err != nil || !strings.Contains(out, "build watch") {
		t.Errorf("list does not show the agent's own task: %q %v", out, err)
	}
	if _, err := call(map[string]any{"action": "move", "name": "build watch", "to": "cortex"}); err != nil || rec.moved[0]["id"] != "t1" || rec.moved[0]["to"] != "cortex" {
		t.Errorf("move by name did not reach the task: %v %v", rec.moved, err)
	}
	if _, err := call(map[string]any{"action": "delete", "name": "Build Watch"}); err != nil || rec.cancelled[0]["id"] != "t1" {
		t.Errorf("delete by name did not reach the task: %v %v", rec.cancelled, err)
	}
	if _, err := call(map[string]any{"action": "delete", "name": "t9", "agent": "Helper"}); err != nil || rec.cancelled[1]["agent"] != "Helper" || rec.cancelled[1]["id"] != "t9" {
		t.Errorf("another agent's task was not reached through its scope: %v %v", rec.cancelled, err)
	}
	if _, err := call(map[string]any{"action": "pause", "name": "build watch"}); err == nil {
		t.Error("a recurring task was paused; it has no pause")
	}
}

// An author keeps both: no agent named means a task of its own, an agent
// named means a fleet schedule on that agent.
func TestAuthorScheduleFoldsBothPaths(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	rec := &recordingRecurring{}
	var tool AgentToolDef
	for _, td := range operatorManagementToolsFor(&ToolSession{Username: "craig"}, "seed-builder", rec) {
		if td.Tool.Name == "schedule" {
			tool = td
		}
	}
	if tool.Tool.Name == "" {
		t.Fatal("schedule is gone")
	}
	if _, ok := tool.Tool.Parameters["url"]; !ok {
		t.Error("an author lost the monitor whens")
	}
	if _, ok := tool.Tool.Parameters["to"]; !ok {
		t.Error("an author lost its own task's report target")
	}
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }
	if _, err := call(map[string]any{"when": "every", "time": "hourly", "then": "poll the thing"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.scheduled) != 1 || len(ListStandingAgents(db, "craig")) != 0 {
		t.Errorf("no agent named should be the author's own task: own=%d standing=%d", len(rec.scheduled), len(ListStandingAgents(db, "craig")))
	}
	if _, err := call(map[string]any{"when": "every", "time": "hourly", "then": "poll the thing", "agent": "agent-2", "name": "poller"}); err != nil {
		t.Fatal(err)
	}
	if sa, ok := GetStandingAgent(db, "craig", "poller"); !ok || sa.AgentID != "agent-2" {
		t.Errorf("an agent named should be a fleet schedule on it: %+v %v", sa, ok)
	}
	// A weekday pattern the own path cannot keep falls through to the fleet,
	// on the asking agent.
	if _, err := call(map[string]any{"when": "every", "time": "weekdays 17:00", "then": "wrap up", "name": "wrap"}); err != nil {
		t.Fatal(err)
	}
	if sa, ok := GetStandingAgent(db, "craig", "wrap"); !ok || sa.AgentID != "seed-builder" || sa.Cron != "weekdays 17:00" {
		t.Errorf("weekday pattern did not fall through to a fleet schedule: %+v %v", sa, ok)
	}
}

// The model sees one scheduling tool everywhere: a Fleet agent's catalog
// holds schedule without recurring folded in, and a plain agent's holds the
// recurring-only schedule, never a second tool beside it.
func TestOneSchedulingToolPerAgent(t *testing.T) {
	for _, old := range []string{"recurring", "create_standing_agent", "create_event_monitor", "set_timer"} {
		if IsReservedToolName("schedule") && canonicalToolName(old) != "schedule" && old != "recurring" {
			t.Errorf("%s does not alias to schedule", old)
		}
	}
	if !IsReservedToolName("schedule") {
		t.Error("schedule is not a reserved name: a custom tool could shadow it")
	}
}
