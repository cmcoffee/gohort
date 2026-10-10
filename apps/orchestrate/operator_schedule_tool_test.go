package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// One field, the user's words: the agent never chooses between cron and an
// interval.
func TestParseEverySpec(t *testing.T) {
	cases := []struct {
		in       string
		cron     string
		interval int
	}{
		{"every 15 minutes", "", 900},
		{"every 2 hours", "", 7200},
		{"15 min", "", 900},
		{"90s", "", 90},
		{"hourly", "", 3600},
		{"every minute", "", 60},
		{"daily 08:00", "daily 08:00", 0},
		{"every day at 8am", "daily 08:00", 0},
		{"everyday 17:30", "daily 17:30", 0},
		{"weekdays 17:00", "weekdays 17:00", 0},
		{"every weekday at 5pm", "weekdays 17:00", 0},
		{"weekends 10:00", "weekends 10:00", 0},
		{"FRI 21:30", "FRI 21:30", 0},
		{"fri at 9:30 pm", "FRI 21:30", 0},
		{"MON,WED,FRI 09:00", "MON,WED,FRI 09:00", 0},
		{"08:00", "daily 08:00", 0},
		{"8am", "daily 08:00", 0},
		{"every morning at 8", "daily 08:00", 0},
		{"every evening at 6", "daily 18:00", 0},
		{"every night at 11:30", "daily 23:30", 0},
		{"noon", "daily 12:00", 0},
	}
	for _, c := range cases {
		cron, interval, err := parseEverySpec(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if cron != c.cron || interval != c.interval {
			t.Errorf("%q: got cron %q interval %d, want %q %d", c.in, cron, interval, c.cron, c.interval)
		}
	}
	for _, bad := range []string{"", "whenever", "*/5 * * * *", "daily", "every", "funday 10:00"} {
		if cron, interval, err := parseEverySpec(bad); err == nil {
			t.Errorf("%q was read as cron %q interval %d; it is not a schedule", bad, cron, interval)
		}
	}
}

// when="every" with nothing but the words becomes a standing agent run by
// the asking agent: no agent id, no cron/interval choice, no name.
func TestScheduleEveryMakesAStandingAgentRunByTheAsker(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	out, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "every 15 minutes", "then": "fetch the price and tell me",
	})
	if err != nil {
		t.Fatal(err)
	}
	list := ListStandingAgents(db, "craig")
	if len(list) != 1 {
		t.Fatalf("expected 1 standing agent, got %d: %s", len(list), out)
	}
	sa := list[0]
	if sa.AgentID != "agent-1" || sa.IntervalSeconds != 900 || sa.Mission != "fetch the price and tell me" {
		t.Errorf("standing agent wrong: %+v", sa)
	}
	if sa.Name == "" {
		t.Error("no name was made up")
	}
	out, err = tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "daily 08:00", "then": "summarize the overnight alerts", "name": "morning-alerts", "agent": "agent-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := GetStandingAgent(db, "craig", "morning-alerts")
	if !ok || got.Cron != "daily 08:00" || got.AgentID != "agent-2" {
		t.Errorf("named clock schedule wrong: %+v (%s)", got, out)
	}
	// A count of alerts makes no sense on a clock; the refusal says what does.
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "hourly", "then": "x", "stop_after": 2,
	}); err == nil || !strings.Contains(err.Error(), "until") {
		t.Errorf("stop_after on a clock schedule was not redirected: %v", err)
	}
}

// Each monitor when reaches the kind it stands for, with the agent's words
// as the brief, and the old kind names still work as aliases.
func TestScheduleMonitorWhens(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	calls := []struct {
		args map[string]any
		kind string
	}{
		{map[string]any{"when": "value_crosses", "name": "nvda", "url": "https://example.com/q", "json_path": "price", "compare_op": "<", "threshold": "150", "then": "tell me NVDA dropped", "check_every": "5 minutes"}, EventKindHTTP},
		{map[string]any{"when": "output_changes", "name": "roster", "tool_name": "read_chat", "tool_args": map[string]any{"chat_id": "c1"}, "then": "say who joined", "stop_after": 2}, EventKindWatch},
		{map[string]any{"when": "posted", "name": "hook", "then": "relay it"}, EventKindWebhook},
		{map[string]any{"when": "agent_says", "name": "fuzzy", "check": "is the site down? answer YES or NONE", "then": "tell me the site is down", "check_every": "15 minutes"}, EventKindPoll},
		{map[string]any{"when": "webhook", "name": "hook-alias", "then": "relay it"}, EventKindWebhook},
	}
	for _, c := range calls {
		if _, err := tool.Handler(context.Background(), c.args); err != nil {
			t.Errorf("%v: %v", c.args["when"], err)
			continue
		}
		m, ok := GetEventMonitor(db, "craig", c.args["name"].(string))
		if !ok || m.Kind != c.kind || m.WakeBrief != c.args["then"].(string) {
			t.Errorf("%v: got %+v", c.args["when"], m)
		}
		switch c.kind {
		case EventKindHTTP:
			if m.IntervalSeconds != 300 {
				t.Errorf("check_every \"5 minutes\" became %d", m.IntervalSeconds)
			}
		case EventKindWatch:
			if m.MaxFires != 2 {
				t.Errorf("stop_after 2 became %d", m.MaxFires)
			}
		case EventKindPoll:
			if m.CheckAgent != "agent-1" {
				t.Errorf("the checker defaulted to %q, not the asking agent", m.CheckAgent)
			}
		}
	}
	// Missing pieces are named in the ask's own vocabulary.
	for _, c := range []map[string]any{
		{"when": "value_crosses", "then": "x"},
		{"when": "output_changes", "then": "x"},
		{"when": "agent_says", "then": "x"},
		{"when": "at", "then": "x"},
		{"then": "x", "time": "1pm"},
	} {
		if _, err := tool.Handler(context.Background(), c); err == nil {
			t.Errorf("%v was accepted with its essential field missing", c)
		}
	}
}

// pause / resume / run_now / delete take a name and find it wherever it is:
// a clock schedule or a monitor.
func TestScheduleControlFindsEitherKindByName(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }
	if _, err := call(map[string]any{"when": "every", "time": "hourly", "then": "x", "name": "clock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(map[string]any{"when": "posted", "then": "x", "name": "hook"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(map[string]any{"when": "at", "time": "in 2 hours", "then": "x", "name": "later"}); err != nil {
		t.Fatal(err)
	}

	out, err := call(map[string]any{"action": "list"})
	if err != nil || !strings.Contains(out, "clock") || !strings.Contains(out, "hook") || !strings.Contains(out, "later") {
		t.Errorf("list does not show all three: %q %v", out, err)
	}

	if _, err := call(map[string]any{"action": "pause", "name": "hook"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := GetEventMonitor(db, "craig", "hook"); !m.Paused || m.StopReason != MonitorStopOwner {
		t.Errorf("monitor not paused by the owner: %+v", m)
	}
	if _, err := call(map[string]any{"action": "resume", "name": "hook"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := GetEventMonitor(db, "craig", "hook"); m.Paused {
		t.Error("monitor still paused after resume")
	}
	if _, err := call(map[string]any{"action": "pause", "name": "clock"}); err != nil {
		t.Fatal(err)
	}
	if sa, _ := GetStandingAgent(db, "craig", "clock"); !sa.Paused {
		t.Error("standing agent not paused")
	}
	if _, err := call(map[string]any{"action": "delete", "name": "later"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetEventMonitor(db, "craig", "later"); ok {
		t.Error("timer not deleted")
	}
	if _, err := call(map[string]any{"action": "delete", "name": "nothing-here"}); err == nil || !strings.Contains(err.Error(), "list") {
		t.Errorf("a missing name was not refused with a pointer to list: %v", err)
	}
	if _, err := call(map[string]any{"action": "pause"}); err == nil {
		t.Error("pause with no name was accepted")
	}
	// A monitor woken by ANOTHER agent is not this agent's to control.
	SaveEventMonitor(db, EventMonitor{Name: "theirs", Owner: "craig", Kind: EventKindWebhook, WakeAgent: "agent-9"})
	if _, err := call(map[string]any{"action": "delete", "name": "theirs"}); err == nil {
		t.Error("another agent's monitor was deleted")
	}
}

// The model sees one scheduling tool and none of the eight it replaced.
func TestOperatorSetShowsOneSchedulingTool(t *testing.T) {
	seen := map[string]bool{}
	for _, td := range operatorManagementTools(&ToolSession{Username: "craig"}, "agent-1") {
		seen[td.Tool.Name] = true
	}
	if !seen["schedule"] {
		t.Fatal("schedule is not in the operator set")
	}
	for _, old := range scheduleFoldedToolNames() {
		if seen[old] {
			t.Errorf("%s is still shown beside schedule: the pick it removed is back", old)
		}
	}
	for _, keep := range []string{"delegate", "list_runs", "inspect_run", "await_result", "message_contact"} {
		if !seen[keep] {
			t.Errorf("%s went missing in the fold", keep)
		}
	}
}
