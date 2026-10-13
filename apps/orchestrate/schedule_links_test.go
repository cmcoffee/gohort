package orchestrate

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/timing"
	"github.com/cmcoffee/snugforge/kvlite"
)

func linkTestSetup(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	PreInitScheduler()
}

// Two schedules linked share one time: the group starts on the time of the one
// the link was made from, each member's own timer stops while its own time is
// kept, and they run in the order set, one after the other. Unlinking one of
// two dissolves the group and puts both back on their own times.
// Observed: the user wanted two tasks at the same random time, one then the
// other, and "Part of" had no way to say so.
func TestLinkGroupLifecycle(t *testing.T) {
	linkTestSetup(t)
	var mu sync.Mutex
	var order []string
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		mu.Lock()
		order = append(order, sa.Name)
		mu.Unlock()
		return StandingRunResult{Status: RunOK}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil) })

	a := StandingAgent{Owner: "u", Name: "gather", AgentID: "agent-a", Mission: "gather", IntervalSeconds: 1800, MaxIntervalSeconds: 3600}
	b := StandingAgent{Owner: "u", Name: "post", AgentID: "agent-b", Mission: "post", Cron: "daily 09:00"}
	for _, sa := range []StandingAgent{a, b} {
		SaveStandingAgent(RootDB, sa)
		if err := ScheduleStandingAgent(RootDB, sa); err != nil {
			t.Fatal(err)
		}
	}

	if err := linkSchedules("u", "standing:gather", "standing:post"); err != nil {
		t.Fatalf("link: %v", err)
	}
	groups := listLinkGroups("u")
	if len(groups) != 1 {
		t.Fatalf("want one group, got %d", len(groups))
	}
	g := groups[0]
	if g.IntervalSeconds != 1800 || g.MaxIntervalSeconds != 3600 {
		t.Fatalf("the group did not start on gather's time: %+v", g)
	}
	if gap := time.Until(g.NextRun); g.SchedulerID == "" || gap < 29*time.Minute || gap > 61*time.Minute {
		t.Fatalf("the group's time is not armed 30-60 minutes out: %v %s", g.SchedulerID, gap)
	}
	for _, name := range []string{"gather", "post"} {
		sa, _ := GetStandingAgent(RootDB, "u", name)
		if sa.LinkGroup != g.ID || sa.SchedulerID != "" || !sa.NextRun.IsZero() {
			t.Fatalf("%s still runs on its own timer: %+v", name, sa)
		}
	}
	if sa, _ := GetStandingAgent(RootDB, "u", "post"); sa.Cron != "daily 09:00" {
		t.Fatalf("post's own time was not kept: %q", sa.Cron)
	}

	if err := moveInLinkGroup("u", "standing:post", -1); err != nil {
		t.Fatal(err)
	}
	runLinkGroup(context.Background(), "u", g.ID, true)
	if strings.Join(order, ",") != "post,gather" {
		t.Fatalf("members did not run in the set order: %v", order)
	}
	if cur, _ := getLinkGroup("u", g.ID); len(cur.LastRun) != 2 || cur.LastRun[0].Name != "post" {
		t.Fatalf("the group did not record how its run went: %+v", cur.LastRun)
	}

	if err := unlinkSchedule("u", "standing:gather"); err != nil {
		t.Fatal(err)
	}
	if _, ok := getLinkGroup("u", g.ID); ok {
		t.Fatal("a group left with one member was not dissolved")
	}
	for _, name := range []string{"gather", "post"} {
		sa, _ := GetStandingAgent(RootDB, "u", name)
		if sa.LinkGroup != "" || sa.SchedulerID == "" || sa.NextRun.IsZero() {
			t.Fatalf("%s is not back on its own time: %+v", name, sa)
		}
	}
}

// A third schedule joins an existing group at the end, and the picker offers
// the group to join but never a schedule already in one.
func TestLinkJoinAndOptions(t *testing.T) {
	linkTestSetup(t)
	for _, n := range []string{"one", "two", "three"} {
		sa := StandingAgent{Owner: "u", Name: n, AgentID: "agent-a", Mission: n, IntervalSeconds: 3600}
		SaveStandingAgent(RootDB, sa)
	}
	if err := linkSchedules("u", "standing:one", "standing:two"); err != nil {
		t.Fatal(err)
	}
	g := listLinkGroups("u")[0]

	var labels []string
	for _, o := range linkOptions("u", map[string]string{"agent-a": "Alpha"}, "standing:three", "") {
		labels = append(labels, o.Label)
	}
	got := strings.Join(labels, "|")
	if !strings.Contains(got, "Join: one → two") {
		t.Fatalf("the group is not offered to join: %s", got)
	}
	if strings.Contains(got, "|one") || strings.Contains(got, "|two") {
		t.Fatalf("a schedule already in a group was offered: %s", got)
	}

	if err := linkSchedules("u", "standing:three", "link:"+g.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := getLinkGroup("u", g.ID)
	if len(cur.Members) != 3 || cur.Members[2].ID != "three" {
		t.Fatalf("three did not join at the end: %+v", cur.Members)
	}
	if err := linkSchedules("u", "standing:one", "standing:three"); err == nil {
		t.Fatal("a schedule already in a group was linked again")
	}
}

// A linked schedule that is deleted drops out of its group, and a group left
// with one member puts it back on its own time.
func TestLinkGroupPrunesDeletedMembers(t *testing.T) {
	linkTestSetup(t)
	for _, n := range []string{"one", "two"} {
		SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: n, AgentID: "agent-a", Mission: n, IntervalSeconds: 3600})
	}
	if err := linkSchedules("u", "standing:one", "standing:two"); err != nil {
		t.Fatal(err)
	}
	DeleteStandingAgent(RootDB, "u", "two")
	g := listLinkGroups("u")[0]
	linkMu.Lock()
	alive := pruneLinkGroup(&g)
	linkMu.Unlock()
	if alive {
		t.Fatal("a group with one live member survived")
	}
	if sa, _ := GetStandingAgent(RootDB, "u", "one"); sa.LinkGroup != "" {
		t.Fatalf("the remaining member was not put back on its own time: %+v", sa)
	}
}

// A recurring task in a group is parked on a dormant tick with its pattern
// kept, and unlinking puts it back on that pattern.
func TestLinkedRecurringTaskKeepsItsPattern(t *testing.T) {
	linkTestSetup(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "post", AgentID: "agent-a", Mission: "post", IntervalSeconds: 3600})
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s1", AgentID: "agent-a", Username: "u",
		Prompt: "gather", Name: "gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatalf("seed recurring: %v", err)
	}
	rts := listAgentRecurringTasks("u", "")
	if len(rts) != 1 {
		t.Fatalf("want one recurring task, got %d", len(rts))
	}
	if err := linkSchedules("u", "recurring:"+rts[0].TaskID, "standing:post"); err != nil {
		t.Fatalf("link: %v", err)
	}
	g := listLinkGroups("u")[0]
	if g.IntervalSeconds != 7200 {
		t.Fatalf("the group did not take the recurring task's time: %+v", g)
	}
	rts = listAgentRecurringTasks("u", "")
	if len(rts) != 1 || rts[0].Payload.LinkGroup != g.ID || rts[0].Payload.IntervalSeconds != 7200 {
		t.Fatalf("the task is not linked with its pattern kept: %+v", rts)
	}
	if at, _ := time.Parse(time.RFC3339, rts[0].RunAt); time.Until(at) < 23*time.Hour {
		t.Fatalf("a linked task's own tick is not dormant: due %s", rts[0].RunAt)
	}

	if err := unlinkSchedule("u", "recurring:"+rts[0].TaskID); err != nil {
		t.Fatal(err)
	}
	rts = listAgentRecurringTasks("u", "")
	if len(rts) != 1 || rts[0].Payload.LinkGroup != "" {
		t.Fatalf("the task did not leave the group: %+v", rts)
	}
	if at, _ := time.Parse(time.RFC3339, rts[0].RunAt); time.Until(at) > 2*time.Hour+time.Minute {
		t.Fatalf("the task is not back on its own 2h pattern: due %s", rts[0].RunAt)
	}
}

// One agent's Scheduler lists only its own members of a group, and the header
// says how many run from other agents; the all-agents view lists every member,
// labelled with its agent when the group spans agents.
func TestLinkGroupRowsPerAgent(t *testing.T) {
	linkTestSetup(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "gather", AgentID: "agent-a", Mission: "gather", IntervalSeconds: 3600})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "post", AgentID: "agent-b", Mission: "post", IntervalSeconds: 3600})
	if err := linkSchedules("u", "standing:gather", "standing:post"); err != nil {
		t.Fatal(err)
	}
	rows, placed := appendLinkGroupRows(nil, "u", nil, "agent-a")
	if len(rows) != 2 {
		t.Fatalf("agent-a's view should be the header and its one member, got %d rows: %v", len(rows), rows)
	}
	if !rows[0]["_group_head"].(bool) || !strings.Contains(rows[0]["group"].(string), "1 of them another agent's") {
		t.Fatalf("header: %v", rows[0])
	}
	if rows[1]["name"] != "gather" || rows[1]["schedule"] != "step 1 of 2" || rows[1]["_group"] != rows[0]["_group"] {
		t.Fatalf("member: %v", rows[1])
	}
	if rows[1]["_link"] != false || rows[1]["_linked"] != true {
		t.Fatalf("a linked row should offer Unlink and not Link: %v", rows[1])
	}
	if opts := linkOptions("u", nil, "standing:gather", ""); len(opts) != 0 {
		t.Fatalf("a linked schedule was offered somewhere to link: %v", opts)
	}
	if !placed["standing:gather"] || placed["standing:post"] {
		t.Fatalf("placed: %v", placed)
	}

	rows, _ = appendLinkGroupRows(nil, "u", nil, "")
	if len(rows) != 3 {
		t.Fatalf("the all-agents view should list every member, got %d rows", len(rows))
	}
	if name, _ := rows[2]["name"].(string); !strings.HasPrefix(name, "(agent-b) ") {
		t.Fatalf("a member of a cross-agent group is not labelled with its agent: %q", name)
	}
}

// Linked schedules share one note: every member's runs read and write it, a
// member's own note is set aside and back on unlink, a member leaving does not
// take the group's note with it, and the note goes when the group does.
func TestLinkGroupSharesOneNote(t *testing.T) {
	linkTestSetup(t)
	for _, n := range []string{"gather", "post", "third"} {
		SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: n, AgentID: "agent-a", Mission: n, IntervalSeconds: 3600})
	}
	gatherOwn := standingTaskNotes(StandingAgent{Owner: "u", Name: "gather"})
	SaveOperatingNotes(RootDB, gatherOwn.ns, "gather's own")

	if err := linkSchedules("u", "standing:gather", "standing:post"); err != nil {
		t.Fatal(err)
	}
	g := listLinkGroups("u")[0]
	ga, _ := GetStandingAgent(RootDB, "u", "gather")
	pa, _ := GetStandingAgent(RootDB, "u", "post")
	gn, pn := standingTaskNotes(ga), standingTaskNotes(pa)
	if gn.ns != pn.ns || !gn.group {
		t.Fatalf("members do not share the group's note: %q %q", gn.ns, pn.ns)
	}
	SaveOperatingNotes(RootDB, gn.ns, "found 12 new items")
	if got := pn.load().Text; got != "found 12 new items" {
		t.Fatalf("the next step does not see what the first wrote: %q", got)
	}
	if !strings.Contains(pn.block(), "shared by this link group") {
		t.Fatalf("the block does not say it is shared: %s", pn.block())
	}
	if tn, ok := taskNotesFor("u", schedKindLink, g.ID); !ok || tn.ns != gn.ns {
		t.Fatal("the group's Notes button does not reach the shared note")
	}

	if err := linkSchedules("u", "standing:third", "link:"+g.ID); err != nil {
		t.Fatal(err)
	}
	if err := unlinkSchedule("u", "standing:gather"); err != nil {
		t.Fatal(err)
	}
	ga, _ = GetStandingAgent(RootDB, "u", "gather")
	if got := standingTaskNotes(ga).load().Text; got != "gather's own" {
		t.Fatalf("gather's own note did not come back on unlink: %q", got)
	}
	if got := pn.load().Text; got != "found 12 new items" {
		t.Fatalf("a member leaving took the group's note: %q", got)
	}

	if err := unlinkSchedule("u", "standing:post"); err != nil { // leaves one: the group dissolves
		t.Fatal(err)
	}
	if got := pn.load().Text; got != "" {
		t.Fatalf("the shared note outlived its group: %q", got)
	}
}

// Until on a scheduled agent: it ends at its run count or when its next run
// would be past its end date, and says why; the Until section converts an old
// record's timing so it can carry them, and sets the goal.
func TestScheduleEndsAtItsStops(t *testing.T) {
	linkTestSetup(t)
	sa := StandingAgent{Owner: "u", Name: "poster", AgentID: "a", Mission: "x", Cron: "daily 09:00"}
	if err := applyStandingUntil(&sa, untilBody{MaxRuns: 2, Goal: strp("the PR is merged"), MaxAttempts: 4}, true); err != nil {
		t.Fatal(err)
	}
	if !sa.Timing.Set() || sa.Cron != "" || sa.Timing.MaxRuns != 2 || sa.Until != "the PR is merged" || sa.MaxAttempts != 4 {
		t.Fatalf("until not applied: %+v", sa)
	}
	sa.Runs = 2
	SaveStandingAgent(RootDB, sa)
	if err := ScheduleStandingAgent(RootDB, sa); err != nil {
		t.Fatal(err)
	}
	got, _ := GetStandingAgent(RootDB, "u", "poster")
	if !got.Paused || got.StopCause != StoppedByEnd || got.SchedulerID != "" {
		t.Fatalf("did not end at its run count: %+v", got)
	}

	dated := StandingAgent{Owner: "u", Name: "dated", AgentID: "a", Mission: "x",
		Timing: timing.Spec{How: timing.HowDaily, Times: []int{540}, StopAt: time.Now().Add(-time.Minute)}}
	SaveStandingAgent(RootDB, dated)
	if err := ScheduleStandingAgent(RootDB, dated); err != nil {
		t.Fatal(err)
	}
	got, _ = GetStandingAgent(RootDB, "u", "dated")
	if !got.Paused || got.StopCause != StoppedByEnd || !got.NextRun.IsZero() {
		t.Fatalf("did not end at its end date: %+v", got)
	}
}

// A link group ends after its rounds.
func TestLinkGroupEndsAfterItsRounds(t *testing.T) {
	linkTestSetup(t)
	for _, n := range []string{"one", "two"} {
		SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: n, AgentID: "a", Mission: n, IntervalSeconds: 3600})
	}
	if err := linkSchedules("u", "standing:one", "standing:two"); err != nil {
		t.Fatal(err)
	}
	g := listLinkGroups("u")[0]
	sa := g.timing()
	if err := applyStandingUntil(&sa, untilBody{MaxRuns: 1}, false); err != nil {
		t.Fatal(err)
	}
	g.setTiming(sa)
	g.Rounds = 1
	if err := g.arm(); err != nil {
		t.Fatal(err)
	}
	got, _ := getLinkGroup("u", g.ID)
	if !got.Paused || got.EndNote == "" {
		t.Fatalf("the group did not end after its rounds: %+v", got)
	}
}
