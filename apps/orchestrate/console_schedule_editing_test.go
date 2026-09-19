package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func strp(s string) *string { return &s }

// The Scheduler used to let you change WHEN a thing ran and, for two of the
// three kinds, nothing about WHAT it did. These pin the editing half that
// closed that: what each kind will accept, what it refuses, and the states a
// careless edit could otherwise leave behind.

// A monitor's record holds four kinds' worth of condition fields and the editor
// only ever draws one kind's. If "not sent" meant "set to empty", opening the
// modal on a poll monitor would silently blank everything else on the record.
func TestAnAbsentFieldIsNotAnEmptyOne(t *testing.T) {
	m := EventMonitor{
		Kind: EventKindPoll, WakeBrief: "say what broke",
		Check: "is the build red?", MatchContains: "YES",
		IntervalSeconds: 300,
	}
	// A body carrying only the interval — every other field absent.
	if err := applyMonitorUpdate(&m, monitorUpdateBody{IntervalMinutes: 10}); err != nil {
		t.Fatalf("a timing-only edit was refused: %v", err)
	}
	if m.IntervalSeconds != 600 {
		t.Errorf("interval is %d, want 600", m.IntervalSeconds)
	}
	if m.WakeBrief != "say what broke" || m.Check != "is the build red?" || m.MatchContains != "YES" {
		t.Errorf("a timing-only edit rewrote the condition: %+v", m)
	}
}

// An interval of zero is not a schedule anybody meant to set, so absent and
// zero can safely mean the same "leave it".
func TestNoIntervalMeansLeaveTheIntervalAlone(t *testing.T) {
	m := EventMonitor{Kind: EventKindPoll, Check: "c", WakeBrief: "b", IntervalSeconds: 900}
	if err := applyMonitorUpdate(&m, monitorUpdateBody{WakeBrief: strp("a new brief")}); err != nil {
		t.Fatalf("a brief-only edit was refused: %v", err)
	}
	if m.IntervalSeconds != 900 {
		t.Errorf("a brief-only edit reset the interval to %d", m.IntervalSeconds)
	}
	if m.WakeBrief != "a new brief" {
		t.Errorf("the brief did not take: %q", m.WakeBrief)
	}
}

// A field belonging to another kind is named rather than stored. Stored, it
// would be dead weight on the record and live weight in the reader's head: a
// poll monitor carrying a url reads like something that fetches it.
func TestAFieldTheKindDoesNotOwnIsRefusedByName(t *testing.T) {
	m := EventMonitor{Kind: EventKindPoll, Check: "c", WakeBrief: "b", IntervalSeconds: 60}
	err := applyMonitorUpdate(&m, monitorUpdateBody{URL: strp("https://example.invalid")})
	if err == nil {
		t.Fatal("a poll monitor accepted a url")
	}
	if !strings.Contains(err.Error(), "url") || !strings.Contains(err.Error(), EventKindPoll) {
		t.Errorf("the refusal does not say what was wrong: %q", err)
	}
	if m.URL != "" {
		t.Errorf("the refused field was stored anyway: %q", m.URL)
	}
}

// The push-triggered monitor is the reason the editor is offered on every kind
// now: it has no interval, but it has both of the other two halves.
func TestAWebhookHasNoIntervalButStillHasABrief(t *testing.T) {
	m := EventMonitor{Kind: EventKindWebhook, WakeBrief: "old"}
	if err := applyMonitorUpdate(&m, monitorUpdateBody{IntervalMinutes: 5}); err == nil {
		t.Error("a webhook accepted an interval")
	} else if !strings.Contains(err.Error(), "push-triggered") {
		t.Errorf("the refusal does not explain itself: %q", err)
	}
	if err := applyMonitorUpdate(&m, monitorUpdateBody{WakeBrief: strp("new")}); err != nil {
		t.Fatalf("a webhook refused a brief edit: %v", err)
	}
	if m.WakeBrief != "new" {
		t.Errorf("brief is %q", m.WakeBrief)
	}
}

// Each kind's own condition fields go through, and the empty ones that MEAN
// something keep meaning it: match_contains falls back to YES at fire time and
// format_script falls back to the built-in diff, so both are clearable.
func TestEachKindEditsItsOwnCondition(t *testing.T) {
	poll := EventMonitor{Kind: EventKindPoll, Check: "old?", MatchContains: "YEP", WakeBrief: "b"}
	if err := applyMonitorUpdate(&poll, monitorUpdateBody{Check: strp("is it raining?"), MatchContains: strp("")}); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if poll.Check != "is it raining?" || poll.MatchContains != "" {
		t.Errorf("poll condition did not take: %+v", poll)
	}

	http := EventMonitor{Kind: EventKindHTTP, URL: "https://old.invalid", CompareOp: ">", Threshold: "10", WakeBrief: "b"}
	if err := applyMonitorUpdate(&http, monitorUpdateBody{
		URL: strp("https://new.invalid"), JSONPath: strp("result.0.price"), CompareOp: strp("<"), Threshold: strp("42"),
	}); err != nil {
		t.Fatalf("http_poll: %v", err)
	}
	if http.URL != "https://new.invalid" || http.CompareOp != "<" || http.Threshold != "42" || http.JSONPath != "result.0.price" {
		t.Errorf("http_poll condition did not take: %+v", http)
	}

	watch := EventMonitor{Kind: EventKindWatch, ToolName: "bridge_cred_x", FormatScript: "print(1)", WakeBrief: "b"}
	if err := applyMonitorUpdate(&watch, monitorUpdateBody{FormatScript: strp("")}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if watch.FormatScript != "" {
		t.Errorf("clearing the format script left %q, so there is no way back to the built-in diff", watch.FormatScript)
	}
}

// A monitor that fires with nothing to say, or polls without a question, or
// compares with an operator the comparison will refuse hours later into a log
// nobody reads. All three are caught at the edit.
func TestAnEditCannotLeaveAMonitorWithNothingToDo(t *testing.T) {
	cases := []struct {
		name string
		mon  EventMonitor
		body monitorUpdateBody
		says string
	}{
		{"blank brief", EventMonitor{Kind: EventKindPoll, Check: "c", WakeBrief: "b"},
			monitorUpdateBody{WakeBrief: strp("   ")}, "needs a brief"},
		{"blank check", EventMonitor{Kind: EventKindPoll, Check: "c", WakeBrief: "b"},
			monitorUpdateBody{Check: strp("")}, "needs a check"},
		{"blank url", EventMonitor{Kind: EventKindHTTP, URL: "u", CompareOp: ">", Threshold: "1", WakeBrief: "b"},
			monitorUpdateBody{URL: strp("")}, "needs a url"},
		{"blank threshold", EventMonitor{Kind: EventKindHTTP, URL: "u", CompareOp: ">", Threshold: "1", WakeBrief: "b"},
			monitorUpdateBody{Threshold: strp("")}, "needs a threshold"},
		{"nonsense operator", EventMonitor{Kind: EventKindHTTP, URL: "u", CompareOp: ">", Threshold: "1", WakeBrief: "b"},
			monitorUpdateBody{CompareOp: strp("=>")}, "compare_op"},
		{"interval below the floor", EventMonitor{Kind: EventKindPoll, Check: "c", WakeBrief: "b"},
			monitorUpdateBody{IntervalSeconds: 2}, "too small"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := c.mon
			err := applyMonitorUpdate(&c.mon, c.body)
			if err == nil {
				t.Fatal("the edit was accepted")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say why: %q", err)
			}
			// EventMonitor carries a map, so compare the fields an edit
			// could have touched rather than the whole struct.
			if c.mon.WakeBrief != before.WakeBrief || c.mon.Check != before.Check ||
				c.mon.MatchContains != before.MatchContains || c.mon.URL != before.URL ||
				c.mon.JSONPath != before.JSONPath || c.mon.Regex != before.Regex ||
				c.mon.CompareOp != before.CompareOp || c.mon.Threshold != before.Threshold ||
				c.mon.FormatScript != before.FormatScript || c.mon.IntervalSeconds != before.IntervalSeconds {
				t.Errorf("a refused edit changed the record anyway:\n got %+v\nwant %+v", c.mon, before)
			}
		})
	}
}

// The stored edge/baseline state describes the condition that was replaced.
// Left alone, an http_poll whose threshold moved past the current value reports
// a RECOVERY from a breach of a threshold that never existed.
func TestChangingWhatItWatchesForDropsTheOldBaseline(t *testing.T) {
	base := EventMonitor{Kind: EventKindHTTP, URL: "https://x.invalid", CompareOp: ">", Threshold: "100", WakeBrief: "b"}
	moved := base
	moved.Threshold = "200"
	if !monitorConditionChanged(base, moved) {
		t.Error("a moved threshold does not count as a changed condition, so the stale breach flag survives it")
	}
	for _, c := range []struct {
		name string
		edit func(*EventMonitor)
	}{
		{"a new brief", func(m *EventMonitor) { m.WakeBrief = "something else" }},
		{"a new interval", func(m *EventMonitor) { m.IntervalSeconds = 3600 }},
		{"a rewritten format script", func(m *EventMonitor) { m.FormatScript = "print('x')" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			after := base
			c.edit(&after)
			if monitorConditionChanged(base, after) {
				t.Error("this counts as a condition change, so it throws away the baseline and silently eats the next change")
			}
		})
	}
}

// The standing agent's half of the same question: the mission is editable from
// the Scheduler now, and cannot be edited into nothing.
func TestAScheduledAgentsMissionIsEditableButNotErasable(t *testing.T) {
	sa := StandingAgent{Name: "nightly", Mission: "review yesterday", Cron: "daily 09:00"}
	if err := applyStandingUpdate(&sa, standingUpdateBody{Cron: "daily 09:00", Mission: strp("  review last week  ")}); err != nil {
		t.Fatalf("a mission edit was refused: %v", err)
	}
	if sa.Mission != "review last week" {
		t.Errorf("mission is %q, want it trimmed and replaced", sa.Mission)
	}

	// Absent preserves, which is what a timing-only caller sends.
	if err := applyStandingUpdate(&sa, standingUpdateBody{IntervalMinutes: 30}); err != nil {
		t.Fatalf("a timing-only edit was refused: %v", err)
	}
	if sa.Mission != "review last week" {
		t.Errorf("a timing-only edit blanked the mission: %q", sa.Mission)
	}
	if sa.Cron != "" || sa.IntervalSeconds != 1800 {
		t.Errorf("switching to an interval left cron=%q interval=%d", sa.Cron, sa.IntervalSeconds)
	}

	// Sent empty is refused, and leaves the record alone.
	before := sa
	if err := applyStandingUpdate(&sa, standingUpdateBody{IntervalMinutes: 30, Mission: strp("\n  ")}); err == nil {
		t.Error("a scheduled agent accepted an empty mission, so it now fires on time with nothing to do")
	} else if !strings.Contains(err.Error(), "needs a mission") {
		t.Errorf("the refusal does not say why: %q", err)
	}
	if sa.Mission != before.Mission {
		t.Errorf("a refused edit changed the mission anyway: %q", sa.Mission)
	}
}

// Cron beats an interval, matching StandingAgent's own precedence, and the
// loser is CLEARED rather than left behind to disagree with the winner.
func TestOneScheduleWinsAndTheOtherIsCleared(t *testing.T) {
	sa := StandingAgent{Mission: "m", IntervalSeconds: 600}
	if err := applyStandingUpdate(&sa, standingUpdateBody{Cron: "FRI 21:30", IntervalMinutes: 10}); err != nil {
		t.Fatal(err)
	}
	if sa.Cron != "FRI 21:30" || sa.IntervalSeconds != 0 {
		t.Errorf("cron=%q interval=%d: both are set, so the record disagrees with itself", sa.Cron, sa.IntervalSeconds)
	}
	if err := applyStandingUpdate(&sa, standingUpdateBody{}); err == nil {
		t.Error("an edit with no schedule at all was accepted")
	}
}

// The editor is only reachable if the row offers the button. Every monitor now
// has something to edit, including the push-triggered one that has no clock.
func TestEveryMonitorRowOffersTheEditor(t *testing.T) {
	for _, schedulable := range []bool{true, false} {
		row := map[string]any{"_schedulable": schedulable}
		addSchedulerActionFlags(row, schedKindMonitor)
		if row["_edit_monitor"] != true {
			t.Errorf("schedulable=%v: no edit button, so its brief and condition are unreachable from this page", schedulable)
		}
		// Test still belongs only to the kinds that have a check to run.
		if got := row["_test_monitor"] == true; got != schedulable {
			t.Errorf("schedulable=%v: _test_monitor=%v", schedulable, got)
		}
	}
}

// --- what a row says about itself ---------------------------------------

// A streak that is invisible until it parks or backs off leaves a row reading
// "active", a next run well off its cadence, and nothing joining the two.
func TestAFailingScheduleSaysSoWhileItIsStillFailing(t *testing.T) {
	loc := time.UTC
	if got := scheduleFailingLabel(0, 3, time.Time{}, loc); got != "" {
		t.Errorf("a healthy row gained a line: %q", got)
	}
	// A monitor parks at a bound, so the count has something to count towards.
	got := scheduleFailingLabel(2, MonitorFailureThreshold(), time.Time{}, loc)
	if !strings.Contains(got, "2 time(s)") || !strings.Contains(got, "stops at 3") {
		t.Errorf("a monitor's streak does not say what it is counting towards: %q", got)
	}
	// A standing agent or a recurring task backs off instead, so the label
	// carries where the next attempt went rather than a bound.
	at := time.Date(2026, 9, 18, 21, 30, 0, 0, time.UTC)
	got = scheduleFailingLabel(4, 0, at, loc)
	if strings.Contains(got, "stops at") {
		t.Errorf("a backing-off schedule claims a park bound it does not have: %q", got)
	}
	if !strings.Contains(got, "next try") || !strings.Contains(got, "2026-09-18") {
		t.Errorf("a backed-off row does not say when to expect the next word: %q", got)
	}
}

// A paused, parked or push-triggered monitor has a stale NextCheck on the
// record. Reporting it would be a promise it is not going to keep.
func TestOnlyALiveMonitorClaimsANextCheck(t *testing.T) {
	due := time.Now().Add(time.Hour)
	live := EventMonitor{Kind: EventKindPoll, NextCheck: due}
	if monitorNextRun(live) == "" {
		t.Error("a running monitor reports no next check, so it sorts with the stopped ones")
	}
	for _, c := range []struct {
		name string
		mon  EventMonitor
	}{
		{"paused", EventMonitor{Kind: EventKindPoll, NextCheck: due, Paused: true}},
		{"parked", EventMonitor{Kind: EventKindPoll, NextCheck: due, Broken: true}},
		{"push-triggered", EventMonitor{Kind: EventKindWebhook, NextCheck: due}},
		{"never armed", EventMonitor{Kind: EventKindPoll}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := monitorNextRun(c.mon); got != "" {
				t.Errorf("claims a next check of %q", got)
			}
		})
	}
}

// The page answers "what is this going to do on its own". In store order that
// takes reading every date on it.
func TestEachSectionReadsInTheOrderItWillHappen(t *testing.T) {
	row := func(section, name, next string) map[string]any {
		return map[string]any{"_section": section, "name": name, "next_run": next}
	}
	rows := []map[string]any{
		row(schedSectionStanding, "later", "2026-09-18T21:00:00Z"),
		row(schedSectionStanding, "paused", ""),
		row(schedSectionStanding, "soonest", "2026-09-18T09:00:00Z"),
		row(schedSectionMonitors, "watcher", "2026-09-18T08:00:00Z"),
		row(schedSectionRecurring, "task", "2026-09-18T23:00:00Z"),
	}
	sortSchedulerRows(rows)
	var got []string
	for _, r := range rows {
		got = append(got, r["name"].(string))
	}
	want := []string{"soonest", "later", "paused", "task", "watcher"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order is %v, want %v", got, want)
	}
}

// A monitor's 08:00 check is earlier than a standing agent's 09:00 run, and
// must still be drawn under its own heading: the cards layout starts a section
// every time _section changes, so a global sort by time would redraw the
// headings on nearly every row.
func TestSortingNeverScattersTheSections(t *testing.T) {
	rows := []map[string]any{
		{"_section": schedSectionStanding, "name": "a", "next_run": "2026-09-18T09:00:00Z"},
		{"_section": schedSectionMonitors, "name": "b", "next_run": "2026-09-18T08:00:00Z"},
		{"_section": schedSectionStanding, "name": "c", "next_run": "2026-09-18T10:00:00Z"},
	}
	sortSchedulerRows(rows)
	seen := map[string]int{}
	last := ""
	for _, r := range rows {
		s := r["_section"].(string)
		if s != last {
			seen[s]++
			last = s
		}
	}
	for section, runs := range seen {
		if runs != 1 {
			t.Errorf("%s is drawn %d times, so its heading repeats down the page", section, runs)
		}
	}
}

// Sorting a list somebody is aiming at must not shuffle it between refreshes.
func TestTwoRowsDueAtTheSameMomentKeepAFixedOrder(t *testing.T) {
	at := "2026-09-18T09:00:00Z"
	first := []map[string]any{
		{"_section": schedSectionStanding, "name": "zulu", "next_run": at},
		{"_section": schedSectionStanding, "name": "alpha", "next_run": at},
	}
	second := []map[string]any{
		{"_section": schedSectionStanding, "name": "alpha", "next_run": at},
		{"_section": schedSectionStanding, "name": "zulu", "next_run": at},
	}
	sortSchedulerRows(first)
	sortSchedulerRows(second)
	if first[0]["name"] != "alpha" || second[0]["name"] != "alpha" {
		t.Errorf("the same two rows came back in two different orders: %v then %v", first, second)
	}
}

// A stamp that will not parse must read as "no next attempt", not as the epoch
// — which would sort a healthy task to the very top of its section.
func TestAnUnreadableStampIsNoStampAtAll(t *testing.T) {
	if got := parseSchedTime("not a time"); !got.IsZero() {
		t.Errorf("parsed junk as %v", got)
	}
	if got := parseSchedTime(""); !got.IsZero() {
		t.Errorf("parsed empty as %v", got)
	}
	want := time.Date(2026, 9, 18, 21, 30, 0, 0, time.UTC)
	if got := parseSchedTime("2026-09-18T21:30:00Z"); !got.Equal(want) {
		t.Errorf("parsed %v, want %v", got, want)
	}
}
