package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// pauseFixture is one recurring task and one scheduled agent of agent-a,
// linked into one group on the recurring task's time.
// The scheduler's store outlives a test, so each test has its own user.
func pauseFixture(t *testing.T, u string) (taskID string, g linkGroup) {
	t.Helper()
	linkTestSetup(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: u, Name: "post", AgentID: "agent-a", Mission: "post", IntervalSeconds: 3600})
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s-" + u, AgentID: "agent-a", Username: u,
		Prompt: "gather", Name: "gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatalf("seed recurring: %v", err)
	}
	if err := linkSchedules(u, "recurring:"+onlyTask(t, u).TaskID, "standing:post"); err != nil {
		t.Fatalf("link: %v", err)
	}
	return onlyTask(t, u).TaskID, listLinkGroups(u)[0]
}

func onlyTask(t *testing.T, u string) recurringTaskRow {
	t.Helper()
	rts := listAgentRecurringTasks(u, "")
	if len(rts) != 1 {
		t.Fatalf("want one recurring task, got %d", len(rts))
	}
	return rts[0]
}

// A recurring task can be paused and resumed like every other kind. It had no
// pause at all, from the Scheduler or the tool: only delete and move. Paused,
// it waits on the dormant tick and its row says so with a Resume; resumed, it
// is back on its own pattern.
func TestARecurringTaskPausesAndResumes(t *testing.T) {
	linkTestSetup(t)
	const u = "pause-solo"
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s-" + u, AgentID: "agent-a", Username: u,
		Prompt: "gather", Name: "gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatal(err)
	}
	msg, err := setRecurringPaused(u, onlyTask(t, u).TaskID, true)
	if err != nil || !strings.HasPrefix(msg, "Paused \"gather\"") {
		t.Fatalf("pause: %q %v", msg, err)
	}
	rt := onlyTask(t, u)
	if at, _ := time.Parse(time.RFC3339, rt.RunAt); !rt.Payload.Paused || time.Until(at) < 23*time.Hour {
		t.Fatalf("a paused task waits on the dormant tick: %+v due %s", rt.Payload.Paused, rt.RunAt)
	}
	rows := listConsoleRecurringRowsForTest(t, u)
	if len(rows) != 1 || rows[0]["state"] != "paused" || !schedFlag(rows[0], "_unpause_recurring") || schedFlag(rows[0], "_pause_recurring") || schedFlag(rows[0], "_resume_recurring") {
		t.Fatalf("the row says paused and offers Resume (not the parked one): %+v", rows)
	}
	if !schedFlag(rows[0], "_at_rest") {
		t.Error("a paused task is at rest")
	}

	msg, err = setRecurringPaused(u, rt.TaskID, false)
	if err != nil || !strings.HasPrefix(msg, "Resumed \"gather\". Next run:") {
		t.Fatalf("resume: %q %v", msg, err)
	}
	rt = onlyTask(t, u)
	if at, _ := time.Parse(time.RFC3339, rt.RunAt); rt.Payload.Paused || time.Until(at) > 2*time.Hour+time.Minute {
		t.Fatalf("a resumed task is back on its own 2h pattern: %+v due %s", rt.Payload.Paused, rt.RunAt)
	}
	if _, err := setRecurringPaused("someone-else", rt.TaskID, true); err == nil {
		t.Error("another person's task is never found")
	}
}

// listConsoleRecurringRowsForTest is the Scheduler's recurring rows for u,
// with their action flags.
func listConsoleRecurringRowsForTest(t *testing.T, u string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range consoleRecurringRows(u, "") {
		out = append(out, schedulerRow(r, "", schedKindRecurring))
	}
	return out
}

// Pausing one member only skips it when the group runs, and says so; the
// group itself pauses and resumes as one, by the id the listing shows. An
// agent asked to hold linked schedules had no group to pause and offered to
// pause each one, which for a recurring task it could not do at all.
func TestALinkGroupPausesAsOneAndAMemberSaysWhatItMeans(t *testing.T) {
	const u = "pause-group"
	taskID, g := pauseFixture(t, u)
	msg, err := setRecurringPaused(u, taskID, true)
	if err != nil || !strings.Contains(msg, "still runs its other members") || !strings.Contains(msg, g.ID) {
		t.Fatalf("pausing a member names its group and what goes on: %q %v", msg, err)
	}
	ran := 0
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		ran++
		return StandingRunResult{Status: RunOK}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil) })
	runLinkGroup(context.Background(), u, g.ID, true)
	cur, _ := getLinkGroup(u, g.ID)
	if len(cur.LastRun) != 2 || cur.LastRun[0].Status != "skipped" || cur.LastRun[0].Note != "paused" || ran != 1 {
		t.Fatalf("the paused member is skipped and the other runs: %+v ran=%d", cur.LastRun, ran)
	}
	if msg, _ := setRecurringPaused(u, onlyTask(t, u).TaskID, false); strings.Contains(msg, "Next run:") || !strings.Contains(msg, "on the group's time") {
		t.Fatalf("a resumed member runs on the group's time, not a time of its own: %q", msg)
	}

	// The whole group, by its id as the listing writes it.
	listing := linkGroupsListing(u)
	if !strings.Contains(listing, "LINK GROUPS") || !strings.Contains(listing, `"`+g.ID+`"`) || !strings.Contains(listing, "gather") || !strings.Contains(listing, "post") {
		t.Fatalf("the listing names the group, its members and how to pause it:\n%s", listing)
	}
	for _, name := range []string{g.ID, `"` + g.ID + `"`, "link group " + g.ID} {
		if got, ok := linkGroupNamed(u, name); !ok || got.ID != g.ID {
			t.Errorf("%q should name the group", name)
		}
	}
	out, err := scheduleControl(context.Background(), u, "agent-a", nil, &recordingRecurring{}, "pause", g.ID, nil)
	if err != nil || !strings.HasPrefix(out, "Paused link group") {
		t.Fatalf("the tool pauses the group: %q %v", out, err)
	}
	if cur, _ := getLinkGroup(u, g.ID); !cur.Paused {
		t.Fatal("the group is paused")
	}
	if !strings.Contains(linkGroupsListing(u), "paused") {
		t.Error("the listing shows the group paused")
	}
	if out, _ := scheduleControl(context.Background(), u, "agent-a", nil, &recordingRecurring{}, "resume", g.ID, nil); !strings.HasPrefix(out, "Resumed link group") {
		t.Fatalf("and resumes it: %q", out)
	}
}

// The tool reaches a recurring task's pause by its name, and a resumed linked
// scheduled agent no longer reports an empty date as its next run.
func TestTheToolPausesARecurringTaskAndALinkedResumeNamesTheGroup(t *testing.T) {
	const u = "pause-tool"
	_, g := pauseFixture(t, u)
	out, err := scheduleControl(context.Background(), u, "agent-a", nil, &recordingRecurring{}, "pause", "gather", nil)
	if err != nil || !strings.HasPrefix(out, "Paused \"gather\"") {
		t.Fatalf("pause by name: %q %v", out, err)
	}
	if !onlyTask(t, u).Payload.Paused {
		t.Fatal("the task is paused")
	}

	def := setStandingPausedToolDef(u)
	if _, err := def.Handler(context.Background(), map[string]any{"name": "post", "paused": true}); err != nil {
		t.Fatal(err)
	}
	msg, err := def.Handler(context.Background(), map[string]any{"name": "post", "paused": false})
	if err != nil || strings.Contains(msg, "Next run:") || !strings.Contains(msg, g.ID) {
		t.Fatalf("a linked scheduled agent resumes onto its group's time: %q %v", msg, err)
	}
}

// The Scheduler's Pause, Resume and Run now carry their marks beside their
// words, on every kind of row and on a link group's row.
func TestSchedulerControlsCarryTheirMarks(t *testing.T) {
	raw, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	for label, icon := range map[string]string{"Pause": "pause", "Resume": "play", "Run now": "once"} {
		with := strings.Count(string(raw), `{Label: "`+label+`", Icon: "`+icon+`"`)
		without := strings.Count(string(raw), `{Label: "`+label+`", Method:`)
		if with == 0 || without != 0 {
			t.Errorf("%s: %d with the %s mark, %d without", label, with, icon, without)
		}
	}
}
