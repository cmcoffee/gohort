package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/timing"
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
	if sa.AgentID != "agent-1" || StandingScheduleLabel(sa) != "every 15 minutes" || sa.Mission != "fetch the price and tell me" {
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
	if !ok || StandingScheduleLabel(got) != "every day at 08:00" || got.AgentID != "agent-2" {
		t.Errorf("named clock schedule wrong: %+v (%s)", got, out)
	}
	// A clock schedule stops after a number of runs: stop_after rides its
	// timing, as the Scheduler's Until section does.
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "hourly", "then": "x", "name": "twice", "stop_after": 2,
	}); err != nil {
		t.Fatal(err)
	}
	if sa, ok := GetStandingAgent(db, "craig", "twice"); !ok || sa.Timing.MaxRuns != 2 {
		t.Errorf("stop_after did not set a run count: %+v", sa)
	}
	// Days, set times and a last day, in the tool's own words.
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "09:00,13:00", "days": "mon,wed,fri", "stop_on": "2026-12-31", "then": "x", "name": "sets",
	}); err != nil {
		t.Fatal(err)
	}
	if sa, ok := GetStandingAgent(db, "craig", "sets"); !ok || StandingScheduleLabel(sa) != "Mon, Wed, Fri at 09:00, 13:00, until Dec 31" {
		t.Errorf("set times on days with an end: %q", StandingScheduleLabel(sa))
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

// A one-off names the agent it wakes the way an every names who runs it:
// agent="QA Radar", when="at". Without this the one-off woke its author, and
// a Builder asked for one run of the agent it had just built set a timer on
// itself instead.
func TestAOneOffNamesTheAgentItWakes(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	udb := UserDB(db, "craig")
	radar, err := saveAgent(udb, AgentRecord{Name: "QA Radar", Owner: "craig", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	tool := scheduleTool(t, &ToolSession{Username: "craig", DB: udb}, "agent-1")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "at", "name": "radar-once", "time": "in 4 minutes", "then": "write and post the digest", "agent": "QA Radar",
	}); err != nil {
		t.Fatal(err)
	}
	m, ok := GetEventMonitor(db, "craig", "radar-once")
	if !ok || m.Kind != EventKindTimer {
		t.Fatalf("the one-off was not set as a timer: %+v", m)
	}
	if m.WakeAgent != radar.ID {
		t.Fatalf("the one-off should wake QA Radar (%s), wakes %q", radar.ID, m.WakeAgent)
	}
}

// Once a year, the way people say it, becomes a yearly timing; the forms that
// were already understood still read as before.
func TestTheScheduleToolReadsAYearlyDate(t *testing.T) {
	for in, want := range map[string]string{
		"DEC-25 09:00":                 "DEC-25 09:00",
		"every year on Dec 25 at 9am":  "DEC-25 09:00",
		"yearly on December 25th 9:00": "DEC-25 09:00",
		"annually on sept 1 at 8:30pm": "SEP-1 20:30",
		"Mar 3 at 7am":                 "MAR-3 07:00",
	} {
		cron, interval, err := parseEverySpec(in)
		if err != nil || interval != 0 || !strings.EqualFold(cron, want) {
			t.Errorf("%q: %q %d %v, want %q", in, cron, interval, err, want)
			continue
		}
		spec, ok, err := everyTiming("u", map[string]any{}, cron, 0, nil)
		if !ok || err != nil || spec.How != "yearly" {
			t.Errorf("%q: timing %+v %v %v, want yearly", in, spec, ok, err)
		}
	}
	if _, _, err := parseEverySpec("every year on Feb 30 at 9am"); err == nil {
		t.Error("a date no year has was accepted")
	}
	for in, want := range map[string][2]any{
		"every 15 minutes": {"", 900},
		"weekdays 17:00":   {"weekdays 17:00", 0},
		"FRI 21:30":        {"FRI 21:30", 0},
		"daily 08:00":      {"daily 08:00", 0},
	} {
		cron, interval, err := parseEverySpec(in)
		if err != nil || cron != want[0] || interval != want[1] {
			t.Errorf("%q now reads as %q %d %v", in, cron, interval, err)
		}
	}
}

// An agent asked to change "its" scheduled task: a scheduled run in a link
// group that runs as it, set up for it. It listed its schedules and was told
// "Nothing is scheduled" four times while introspect named the run, could not
// read what the run does, and could only change it by re-issuing create,
// which needs a time the link group's member does not have of its own. It
// gave up. Now list shows the runs that run as the agent, with what each
// does, and edit changes only that.

func TestAnAgentSeesAndEditsTheScheduledRunsThatRunAsIt(t *testing.T) {
	linkTestSetup(t)
	SaveStandingAgent(RootDB, StandingAgent{Name: "follow-up engagement", Owner: "u", AgentID: "a1",
		Mission: "Reply to new mentions and comments.", LinkGroup: "g1", Cron: "daily 09:00"})
	SaveStandingAgent(RootDB, StandingAgent{Name: "someone else's run", Owner: "u", AgentID: "b2",
		Mission: "Not a1's to change."})
	SaveEventMonitor(RootDB, EventMonitor{Name: "price-watch", Owner: "u", WakeAgent: "a1",
		Kind: EventKindHTTP, WakeBrief: "tell the user the price"})

	// A plain agent: no fleet tools, its own recurring path only.
	tool := scheduleToolDef(nil, "u", "a1", nil, &recordingRecurring{})
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }

	out, err := call(map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"follow-up engagement", "link group", "Reply to new mentions and comments."} {
		if !strings.Contains(out, want) {
			t.Errorf("list does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "someone else's run") {
		t.Errorf("list shows a run that runs as another agent:\n%s", out)
	}

	if _, err := call(map[string]any{"action": "edit", "name": "follow-up engagement"}); err == nil {
		t.Error("an edit with no then was accepted")
	}
	mission := "Reply to new mentions and comments. List every reply with its full Moltbook link."
	if _, err := call(map[string]any{"action": "edit", "name": "follow-up engagement", "then": mission}); err != nil {
		t.Fatal(err)
	}
	sa, _ := GetStandingAgent(RootDB, "u", "follow-up engagement")
	if sa.Mission != mission {
		t.Errorf("the mission is %q", sa.Mission)
	}
	if sa.LinkGroup != "g1" || sa.Cron != "daily 09:00" {
		t.Errorf("the edit moved its timing: link group %q, cron %q", sa.LinkGroup, sa.Cron)
	}

	if _, err := call(map[string]any{"action": "edit", "name": "someone else's run", "then": "x"}); err == nil {
		t.Error("an agent changed a run that runs as another agent")
	}
	if cur, _ := GetStandingAgent(RootDB, "u", "someone else's run"); cur.Mission != "Not a1's to change." {
		t.Error("the other agent's run was changed")
	}

	if _, err := call(map[string]any{"action": "edit", "name": "price-watch", "then": "tell the user the price and the day's change"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := GetEventMonitor(RootDB, "u", "price-watch"); m.WakeBrief != "tell the user the price and the day's change" {
		t.Errorf("the monitor's brief is %q", m.WakeBrief)
	}
}

// A long mission is shortened in the list, so an agent asked to add one rule
// to it either retyped it from the part it could see, losing the rest, or
// stopped to ask the person to paste it. get reads it in full, and edit
// changes part of it (append, find/replace) without the rest being resent.
func TestAnAgentReadsALongMissionAndChangesPartOfIt(t *testing.T) {
	linkTestSetup(t)
	long := "Run a Moltbook engagement cycle.\n\n" + strings.Repeat("Step: browse the feed and engage thoughtfully. ", 30) + "\n\nFINAL STEP: report what you posted."
	SaveStandingAgent(RootDB, StandingAgent{Name: "Random Moltbook engagement", Owner: "u", AgentID: "a1", Mission: long, LinkGroup: "g1"})
	SaveStandingAgent(RootDB, StandingAgent{Name: "theirs", Owner: "u", AgentID: "b2", Mission: "not yours"})
	tool := scheduleToolDef(nil, "u", "a1", nil, &recordingRecurring{})
	call := func(args map[string]any) (string, error) { return tool.Handler(context.Background(), args) }

	list, _ := call(map[string]any{"action": "list"})
	if strings.Contains(list, "FINAL STEP") || !strings.Contains(list, `schedule(action="get", name="Random Moltbook engagement")`) {
		t.Errorf("the list should shorten the mission and say how to read it all:\n%s", list)
	}
	full, err := call(map[string]any{"action": "get", "name": "Random Moltbook engagement"})
	if err != nil || !strings.Contains(full, "FINAL STEP: report what you posted.") || !strings.Contains(full, "link group") {
		t.Fatalf("get: %v\n%s", err, full)
	}
	if _, err := call(map[string]any{"action": "get", "name": "theirs"}); err == nil {
		t.Error("get read a run that runs as another agent")
	}

	rule := `Style: avoid the "X is just Y wearing a Z hat" construction; say what the thing is plainly.`
	if _, err := call(map[string]any{"action": "edit", "name": "Random Moltbook engagement", "append": rule}); err != nil {
		t.Fatal(err)
	}
	sa, _ := GetStandingAgent(RootDB, "u", "Random Moltbook engagement")
	if !strings.HasPrefix(sa.Mission, long) || !strings.HasSuffix(sa.Mission, rule) {
		t.Errorf("append did not keep the mission and add the rule:\n%s", sa.Mission)
	}

	if _, err := call(map[string]any{"action": "edit", "name": "Random Moltbook engagement", "find": "FINAL STEP: report what you posted.", "replace": "FINAL STEP: report what you posted, with links."}); err != nil {
		t.Fatal(err)
	}
	sa, _ = GetStandingAgent(RootDB, "u", "Random Moltbook engagement")
	if !strings.Contains(sa.Mission, "with links.") || !strings.Contains(sa.Mission, rule) || sa.LinkGroup != "g1" {
		t.Errorf("find/replace changed more or less than the passage:\n%s", sa.Mission)
	}
	for name, args := range map[string]map[string]any{
		"a passage that is not there":   {"find": "no such step", "replace": "x"},
		"a passage that is there twice": {"find": "Step: browse the feed", "replace": "x"},
		"two kinds of change at once":   {"append": "x", "then": "y"},
		"no change at all":              {},
	} {
		args["action"], args["name"] = "edit", "Random Moltbook engagement"
		if _, err := call(args); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if sa2, _ := GetStandingAgent(RootDB, "u", "Random Moltbook engagement"); sa2.Mission != sa.Mission {
		t.Error("a refused edit changed the mission")
	}
}

// Once a month, the way people say it, reads as a monthly timing. An agent
// asked for "near the end of the month" tried "monthly last day 9:00",
// "27th 09:00" and "monthly 28th 09:00", was refused each time, and told the
// user the scheduler could not do it at all.
func TestTheScheduleToolReadsAMonthlyDay(t *testing.T) {
	for in, want := range map[string]string{
		"monthly 28th 09:00":                       "MONTHLY-28 09:00",
		"27th 09:00":                               "MONTHLY-27 09:00",
		"monthly last day 9:00":                    "MONTHLY-LAST 09:00",
		"monthly on the 28th at 9am":               "MONTHLY-28 09:00",
		"every month on the 1st at 8:30pm":         "MONTHLY-1 20:30",
		"each month on the last day at 9am":        "MONTHLY-LAST 09:00",
		"the last day of the month at 9am":         "MONTHLY-LAST 09:00",
		"28th of every month at 9am":               "MONTHLY-28 09:00",
		"the 2nd to last day of each month at 9am": "MONTHLY-LAST2 09:00",
		"monthly on the 3rd-to-last day at 18:00":  "MONTHLY-LAST3 18:00",
		"monthly 15 09:00":                         "MONTHLY-15 09:00",
		"MONTHLY-LAST 09:00":                       "MONTHLY-LAST 09:00",
	} {
		cron, interval, err := parseEverySpec(in)
		if err != nil || interval != 0 || cron != want {
			t.Errorf("%q: %q %d %v, want %q", in, cron, interval, err, want)
			continue
		}
		spec, ok, err := everyTiming("u", map[string]any{}, cron, 0, nil)
		if !ok || err != nil || spec.How != timing.HowMonthly {
			t.Errorf("%q: timing %+v %v %v, want monthly", in, spec, ok, err)
		}
	}
	for _, bad := range []string{"monthly 32nd 09:00", "monthly on the 0th at 9am", "monthly on the 9th to last day at 9am"} {
		if cron, _, err := parseEverySpec(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, cron)
		}
	}
	// A number alone is not a day of the month, and the forms already read
	// still read as before.
	for in, want := range map[string]string{
		"daily 08:00":                 "daily 08:00",
		"FRI 21:30":                   "FRI 21:30",
		"every year on Dec 25 at 9am": "DEC-25 09:00",
	} {
		if cron, _, err := parseEverySpec(in); err != nil || cron != want {
			t.Errorf("%q now reads as %q %v", in, cron, err)
		}
	}
	if cron, _, err := parseEverySpec("28 09:00"); err == nil && cron != "" && cron[:7] == "MONTHLY" {
		t.Errorf("a bare number read as monthly: %q", cron)
	}
}

// Through the whole tool: a fleet schedule fires on its day and says so, and
// an agent's own recurring task carries the monthly timing (the pattern
// fields are only its time of day).
func TestAMonthlyScheduleIsMade(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "monthly on the last day at 9am", "then": "post next month's big PC releases", "name": "release-roundup",
	}); err != nil {
		t.Fatal(err)
	}
	sa, ok := GetStandingAgent(db, "craig", "release-roundup")
	if !ok || StandingScheduleLabel(sa) != "every month on the last day at 09:00" {
		t.Fatalf("monthly schedule wrong: %+v (%q)", sa, StandingScheduleLabel(sa))
	}
	from := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	eff, _ := sa.EffectiveTiming()
	if next, err := eff.Next(from, nil); err != nil || !next.Equal(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("next run %v %v, want Nov 30 09:00", next, err)
	}
	// Its stored cron reads the same way where only the cron is looked at.
	if sa.Cron != "" {
		if next, err := NextCronOccurrence(sa.Cron, from); err != nil || !next.Equal(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("cron %q next %v %v", sa.Cron, next, err)
		}
	}

	rec := &recordingRecurring{}
	own := scheduleToolDef(nil, "craig", "a1", nil, rec)
	if _, err := own.Handler(context.Background(), map[string]any{
		"when": "every", "time": "monthly 28th 09:00", "then": "post the roundup", "name": "roundup",
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.scheduled) != 1 {
		t.Fatalf("the recurring path was not used: %v", rec.scheduled)
	}
	got := rec.scheduled[0]
	spec, ok, err := timingArg(got)
	if !ok || err != nil || spec.How != timing.HowMonthly || spec.Day != 28 || spec.Times[0] != 540 {
		t.Errorf("the recurring task lost its monthly timing: %v (%+v %v)", got, spec, err)
	}
	if got["pattern"] != RecurringDaily || got["daily_at"] != "09:00" {
		t.Errorf("pattern fields: %v", got)
	}
}
