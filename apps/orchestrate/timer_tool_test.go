package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

func TestParseTimerAt(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	now := time.Date(2026, 10, 10, 13, 6, 0, 0, loc)
	want := func(h, m, dayOffset int) time.Time {
		return time.Date(2026, 10, 10+dayOffset, h, m, 0, 0, loc)
	}
	cases := []struct {
		in   string
		want time.Time
	}{
		{"13:10", want(13, 10, 0)},
		{"1:10pm", want(13, 10, 0)},
		{"1:10 PM", want(13, 10, 0)},
		{"3 pm", want(15, 0, 0)},
		{"12:30am", want(0, 30, 1)}, // past today, so tomorrow
		{"9:00", want(9, 0, 1)},     // past today, so tomorrow
		{"12pm", want(12, 0, 1)},    // past today, so tomorrow
		{"in 20 minutes", now.Add(20 * time.Minute)},
		{"in 1h30m", now.Add(90 * time.Minute)},
		{"in 90s", now.Add(90 * time.Second)},
		{"in 2 hours 15 min", now.Add(135 * time.Minute)},
		{"2026-10-10T13:10:00-07:00", want(13, 10, 0)},
		{"2026-10-11 08:00", want(8, 0, 1)},
	}
	for _, c := range cases {
		got, err := parseTimerAt(c.in, now)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "soon", "25:00", "13", "in", "in a bit", "2026-10-10T13:00:00-07:00", "0pm", "13pm"} {
		if got, err := parseTimerAt(bad, now); err == nil {
			t.Errorf("%q was read as %s; it is not a usable time", bad, got)
		}
	}
}

// The whole tool is a time and a note: it creates a one-shot timer monitor in
// the owner's zone, armed, with nothing to fetch and nothing to compare.
func TestSetTimerCreatesAnArmedOneShotTimer(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig", ChatSessionID: "sess-1"}, "agent-1")
	for _, p := range []string{"kind", "interval_seconds", "cron", "wake_brief", "mission", "note", "at"} {
		if _, ok := tool.Tool.Parameters[p]; ok {
			t.Errorf("schedule takes %q: a retired tool's own vocabulary leaked into the one surface", p)
		}
	}
	out, err := tool.Handler(context.Background(), map[string]any{"when": "at", "time": "in 4 minutes", "then": "tell the user it's 1:10pm"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "4 minutes from now") || !strings.Contains(out, "fires once") {
		t.Errorf("the confirmation does not say when or that it is one-shot: %q", out)
	}
	ms := ListEventMonitors(db, "craig")
	if len(ms) != 1 {
		t.Fatalf("expected 1 monitor, got %d", len(ms))
	}
	m := ms[0]
	if m.Kind != EventKindTimer || m.MaxFires != 1 || m.FireAt.IsZero() || m.WakeBrief != "tell the user it's 1:10pm" {
		t.Errorf("timer record wrong: %+v", m)
	}
	if m.WakeAgent != "agent-1" || m.WakeSession != "sess-1" {
		t.Errorf("the timer wakes %q in %q, not the agent and session that set it", m.WakeAgent, m.WakeSession)
	}
	if m.SchedulerID == "" || !m.NextCheck.Equal(m.FireAt) {
		t.Errorf("timer not armed at its moment: scheduler %q next %s fire %s", m.SchedulerID, m.NextCheck, m.FireAt)
	}
	if d := time.Until(m.FireAt); d < 3*time.Minute || d > 5*time.Minute {
		t.Errorf("fires in %s, want about 4 minutes", d)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"when": "at", "time": "in 4 minutes", "then": "again"}); err == nil {
		t.Error("a second timer with the same generated name was accepted")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"when": "at", "time": "whenever", "then": "x"}); err == nil {
		t.Error("an unreadable time was accepted")
	}
	// The retired names still resolve for an allowlist written before the fold.
	for _, old := range scheduleFoldedToolNames() {
		if canonicalToolName(old) != "schedule" {
			t.Errorf("%s does not alias to schedule: an agent whose allowlist names it loses scheduling", old)
		}
	}
}

// scheduleTool finds the one scheduling tool in the operator set.
func scheduleTool(t *testing.T, sess *ToolSession, agentID string) AgentToolDef {
	t.Helper()
	for _, td := range operatorManagementTools(sess, agentID) {
		if td.Tool.Name == "schedule" {
			return td
		}
	}
	t.Fatal("schedule is gone")
	return AgentToolDef{}
}

// schedule refuses the monitor that was built for this: a numeric comparison
// against a datetime string, which cannot compare and would have been parked
// as failing before the time came.
func TestCreateEventMonitorRefusesAnUncomparableThreshold(t *testing.T) {
	pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	_, err := tool.Handler(context.Background(), map[string]any{
		"when": "value_crosses", "name": "notify-110pm", "url": "https://example.com/time",
		"json_path": "datetime", "compare_op": ">=", "threshold": "2026-10-10 13:10:00", "stop_after": 1,
	})
	if err == nil {
		t.Fatal("a datetime threshold under >= was accepted; every check of it fails")
	}
	if !strings.Contains(err.Error(), "timer") {
		t.Errorf("the refusal does not point at set_timer: %v", err)
	}
	if ms := ListEventMonitors(RootDB, "craig"); len(ms) != 0 {
		t.Errorf("the refused monitor was saved anyway: %d", len(ms))
	}
}
