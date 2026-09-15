package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pacing"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Who gets the lever. An ordinary recurring task must not: without a
// completion check "come back later" has no way to ever stop, and the cadence
// is that task's whole contract. A manual Run now must not either, because that
// path deliberately leaves the schedule alone and is how an owner retries a
// stalled objective.
func TestPacingToolIsMountedOnlyForAScheduledObjective(t *testing.T) {
	objective := orchUpdatePayload{Username: "u", Until: "the post is live"}
	plain := orchUpdatePayload{Username: "u"}

	if got := pacingTool(objective, &pacing.Ask{}, true); len(got) != 1 {
		t.Fatalf("a scheduled objective should carry the tool, got %d", len(got))
	} else if got[0].Tool.Name != pacing.ToolName {
		t.Errorf("mounted %q, wanted %q", got[0].Tool.Name, pacing.ToolName)
	}
	if got := pacingTool(plain, &pacing.Ask{}, true); len(got) != 0 {
		t.Error("a task with no objective was given a way to defer itself forever")
	}
	if got := pacingTool(objective, &pacing.Ask{}, false); len(got) != 0 {
		t.Error("Run now was given the tool; that path does not touch the schedule")
	}
}

// The ceiling that is easy to miss: a next fire armed past the idle horizon is
// reaped before it ever runs, so an attempt allowed to pace past it would not be
// waiting, it would be deleting the task.
func TestPacingCeilingStopsShortOfTheReap(t *testing.T) {
	if max, why := pacingCeiling(90); max != maxPacingDelay || why != "" {
		t.Errorf("a distant reap should leave the week cap alone, got %s (%q)", max, why)
	}
	max, why := pacingCeiling(3)
	if want := 3*24*time.Hour - pacingReapMargin; max != want {
		t.Fatalf("ceiling %s, wanted %s (the reap horizon, less a margin)", max, want)
	}
	if !strings.Contains(why, "idle day") {
		t.Errorf("the reason must name the reap so a clamped model learns something: %q", why)
	}
	if max, _ := pacingCeiling(0); max != maxPacingDelay {
		t.Errorf("a disabled reap should leave the week cap, got %s", max)
	}
}

// Pacing buys a different hour, never an hour the owner said no to.
func TestPacingDefersIntoTheActiveWindow(t *testing.T) {
	if pacingAdjust(orchUpdatePayload{}, time.Now()) != nil {
		t.Error("a task with no window needs no adjustment")
	}
	// 09:00-17:00 local.
	p := orchUpdatePayload{HasWindow: true, WindowFromMin: 9 * 60, WindowToMin: 17 * 60}
	now := time.Date(2026, 9, 14, 16, 0, 0, 0, time.Local)
	adjust := pacingAdjust(p, now)
	if adjust == nil {
		t.Fatal("a windowed task got no adjustment")
	}
	at := time.Date(2026, 9, 14, 23, 0, 0, 0, time.Local) // outside the window
	got := adjust(at)
	if !got.After(at) {
		t.Fatalf("a time outside the window was left where it was: %s", got)
	}
	if h := got.Hour(); h < 9 || h >= 17 {
		t.Errorf("deferred to %s, which is still outside 09:00-17:00", got.Format("15:04"))
	}
}

// Moving a clock is not progress toward a goal. The checker's rule is that the
// ACTIONS are the evidence, so an attempt that ran nothing but the pacing call
// has to read as one that ran nothing.
func TestPacingIsNotEvidenceOfWork(t *testing.T) {
	labels, failed := objectiveToolLabels([]PersistedToolCall{
		{Name: pacing.ToolName, Args: map[string]any{"minutes": 60}},
	})
	if len(labels) != 0 {
		t.Fatalf("the pacing call reached the checker as work: %v", labels)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	// It is filtered, not everything-after-it dropped.
	labels, _ = objectiveToolLabels([]PersistedToolCall{
		{Name: "moltbook", Args: map[string]any{"action": "get_feed"}},
		{Name: pacing.ToolName},
		{Name: "moltbook", Args: map[string]any{"action": "create_post"}},
	})
	if strings.Join(labels, ",") != "moltbook/get_feed,moltbook/create_post" {
		t.Errorf("real work was dropped alongside the pacing call: %v", labels)
	}
}

// The Next run cell shows WHEN. The State cell has to carry the half it cannot:
// that an attempt chose the time, and what it is waiting for.
func TestPacedStateSaysWhatItIsWaitingFor(t *testing.T) {
	p := orchUpdatePayload{
		Until:          "the release notes are published",
		Attempts:       []ObjectiveAttempt{{Reason: "the build was still running"}},
		NextAttemptWhy: "the build finishes around 14:00",
	}
	got := objectiveStateLabel(p.objective())
	for _, want := range []string{"not yet", "waiting:", "the build finishes around 14:00"} {
		if !strings.Contains(got, want) {
			t.Errorf("state %q is missing %q", got, want)
		}
	}
	p.NextAttemptWhy = ""
	if strings.Contains(objectiveStateLabel(p.objective()), "waiting") {
		t.Error("an unpaced objective claims to be waiting")
	}
}

// applyPacing against a real queue: the successor is already armed, so this
// MOVES an entry rather than scheduling one.
func TestApplyPacingMovesTheArmedSuccessor(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	PreInitScheduler()
	t.Cleanup(func() { RootDB = saved })

	p := orchUpdatePayload{Username: "u", SessionID: "s1", Prompt: "publish the notes", Until: "the notes are live"}
	armedAt := time.Now().Add(time.Hour)
	armedID, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, armedAt)
	if err != nil {
		t.Fatalf("arming the successor: %v", err)
	}

	ask := &pacing.Ask{}
	want := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	tool := pacing.Tool(pacing.ToolSpec{Ask: ask, Min: time.Minute, Max: 24 * time.Hour, Now: func() time.Time { return want.Add(-5 * time.Hour) }})
	if _, herr := tool.Handler(t.Context(), map[string]any{"minutes": 300, "why": "the build is still running"}); herr != nil {
		t.Fatalf("ask: %v", herr)
	}

	armed := p
	line := applyPacing(p, &armed, armedID, ask)
	if line == "" {
		t.Fatal("applyPacing reported nothing; the card would say the next run moved for no reason")
	}
	if !strings.Contains(line, "the build is still running") {
		t.Errorf("the card line drops the attempt's own words: %q", line)
	}
	if armed.NextAttemptWhy != "the build is still running" || armed.NextAttemptAt == "" {
		t.Errorf("the payload did not record the pacing: %+v", armed)
	}
	if armed.LastActive == "" {
		t.Error("pacing must renew the idle clock, or a long wait reaps the task it is waiting for")
	}

	moved := false
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if task.ID != armedID {
			continue
		}
		moved = true
		at, perr := time.Parse(time.RFC3339, task.RunAt)
		if perr != nil {
			t.Fatalf("RunAt unreadable: %q", task.RunAt)
		}
		if at.Sub(want).Abs() > time.Minute {
			t.Errorf("successor sits at %s, wanted about %s", at, want)
		}
	}
	if !moved {
		t.Fatal("the armed successor is gone from the queue")
	}

	// A successor that already fired is not re-created: that is how chains
	// duplicate.
	UnscheduleTask(armedID)
	armed2 := p
	if line := applyPacing(p, &armed2, armedID, ask); line != "" {
		t.Errorf("a consumed successor reported a move: %q", line)
	}
	if armed2.NextAttemptWhy != "" {
		t.Error("a failed move still wrote the payload, so the console would claim a pacing that did not happen")
	}
	// By id, not by queue length: PreInitScheduler is first-writer-wins across a
	// package's tests, so the queue is shared with whatever else scheduled
	// something. What matters is that THIS task did not come back.
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if task.ID == armedID {
			t.Fatal("consumed successor resurrected")
		}
	}
}

// An ask with no successor to move is dropped, not carried.
func TestApplyPacingWithNothingArmedIsANoOp(t *testing.T) {
	ask := &pacing.Ask{}
	tool := pacing.Tool(pacing.ToolSpec{Ask: ask, Min: time.Minute, Max: time.Hour})
	if _, err := tool.Handler(t.Context(), map[string]any{"minutes": 30, "why": "waiting"}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	armed := orchUpdatePayload{}
	if line := applyPacing(orchUpdatePayload{Username: "u"}, &armed, "", ask); line != "" {
		t.Errorf("pacing with no armed successor reported %q", line)
	}
	if armed.NextAttemptAt != "" {
		t.Error("payload written for a move that could not happen")
	}
}

// The standing half. Nothing is moved here: the successor does not exist while
// the fire runs, so the ask is written onto the record for the deferred re-arm
// to find (core.ScheduleStandingAgent honours it).
func TestStandingPacingWritesTheAskOntoTheRecord(t *testing.T) {
	sa := StandingAgent{
		Owner: "u", Name: "release-notes",
		Until:    "the notes are published",
		Attempts: []ObjectiveAttempt{{At: "2026-09-14T09:00:00Z", Reason: "the build was still running"}},
	}
	ask := &pacing.Ask{}
	tool := pacing.Tool(pacing.ToolSpec{Ask: ask, Min: time.Minute, Max: 24 * time.Hour})
	if _, err := tool.Handler(t.Context(), map[string]any{"minutes": 120, "why": "the build finishes around 14:00"}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	line := applyStandingPacing(&sa, ask)
	if line == "" || !strings.Contains(line, "the build finishes around 14:00") {
		t.Fatalf("summary line missing the attempt's own words: %q", line)
	}
	if sa.NextAttemptAt.IsZero() || sa.NextAttemptWhy != "the build finishes around 14:00" {
		t.Fatalf("the record does not carry the ask: %+v", sa)
	}
	// The attempt that asked has to say so, or the next fire reads a gap in the
	// timestamps and cannot tell a slow schedule from a chosen wait.
	if got := sa.Attempts[len(sa.Attempts)-1].NextAt; got == "" {
		t.Error("the attempt was not stamped with the time it asked for")
	}
}

func TestStandingPacingToolIsMountedOnlyForAnObjective(t *testing.T) {
	objective := StandingAgent{Owner: "u", Name: "n", Until: "the notes are published"}
	if got := standingPacingTool(objective, &pacing.Ask{}); len(got) != 1 {
		t.Fatalf("a standing objective should carry the tool, got %d", len(got))
	}
	if got := standingPacingTool(StandingAgent{Owner: "u", Name: "n"}, &pacing.Ask{}); len(got) != 0 {
		t.Error("a standing agent with no objective was given a way to defer itself forever")
	}
}

// What makes a fifth attempt different from a first, one step on: not only what
// was tried, but that the last attempt CHOSE to wait, and until when.
func TestAttemptsBlockShowsAChosenWait(t *testing.T) {
	p := orchUpdatePayload{
		Username: "u",
		Until:    "the notes are published",
		Attempts: []ObjectiveAttempt{
			{At: "2026-09-14T09:00:00Z", Reason: "create_post returned 401"},
			{At: "2026-09-14T10:00:00Z", Reason: "the build was still running", NextAt: "2026-09-14T14:00:00Z"},
		},
	}
	block := objectiveAttemptsBlock(p.objective())
	if !strings.Contains(block, "asked to resume") {
		t.Fatalf("the block does not say the wait was chosen:\n%s", block)
	}
	if strings.Count(block, "asked to resume") != 1 {
		t.Errorf("only the attempt that asked should say so:\n%s", block)
	}
	// In the OWNER's zone, like every other timestamp in the block: a resume
	// time the reader has to convert is one they will convert wrong.
	want := time.Date(2026, 9, 14, 14, 0, 0, 0, time.UTC).In(UserLocation("u")).Format("2006-01-02 15:04")
	if !strings.Contains(block, want) {
		t.Errorf("the block does not say until when (%s):\n%s", want, block)
	}
}

// The monitor half. The asker is not the check (a poll has no turn in it) but
// the AGENT the check woke, so this is a snooze: "still in review, don't look
// again until tomorrow".
func TestMonitorPacingToolIsMountedOnlyWhereItCanWork(t *testing.T) {
	watch := EventMonitor{Owner: "craig", Name: "pr-12", Kind: EventKindWatch, Until: "the PR is merged", IntervalSeconds: 900}
	if got := monitorPacingTool(watch, &pacing.Ask{}); len(got) != 1 {
		t.Fatalf("a scheduled monitor with a goal should carry the tool, got %d", len(got))
	}
	noGoal := watch
	noGoal.Until = ""
	if got := monitorPacingTool(noGoal, &pacing.Ask{}); len(got) != 0 {
		t.Error("a monitor with no goal was given a way to defer itself forever")
	}
	hook := watch
	hook.Kind = EventKindWebhook
	if got := monitorPacingTool(hook, &pacing.Ask{}); len(got) != 0 {
		t.Error("a webhook monitor has no timer to move; the tool would be a button that does nothing")
	}
}

// A snooze is by definition later than the next check would have been. Asking
// for sooner is asking for nothing, and must not become a way to poll something
// faster than its owner set it to.
func TestMonitorPacingCannotPollFaster(t *testing.T) {
	m := EventMonitor{Owner: "craig", Name: "pr-12", Kind: EventKindWatch, Until: "merged", IntervalSeconds: 3600}
	ask := &pacing.Ask{}
	tool := monitorPacingTool(m, ask)[0]
	if _, err := tool.Handler(t.Context(), map[string]any{"minutes": 2, "why": "check again shortly"}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	at, _, _ := ask.Get()
	if d := time.Until(at); d < 59*time.Minute {
		t.Fatalf("a 2-minute ask was honoured on an hourly monitor (%s); the floor is its own cadence", d)
	}
}

func TestApplyMonitorPacingWritesTheAsk(t *testing.T) {
	m := EventMonitor{
		Owner: "craig", Name: "pr-12", Kind: EventKindWatch, Until: "the PR is merged", IntervalSeconds: 900,
		Attempts: []ObjectiveAttempt{{At: "2026-09-14T09:00:00Z", Reason: "still open, one review pending"}},
	}
	ask := &pacing.Ask{}
	tool := monitorPacingTool(m, ask)[0]
	if _, err := tool.Handler(t.Context(), map[string]any{"minutes": 240, "why": "the review is booked for this afternoon"}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	line := applyMonitorPacing(&m, ask)
	if !strings.Contains(line, "the review is booked") {
		t.Fatalf("the line drops the agent's own words: %q", line)
	}
	if m.NextAttemptAt.IsZero() || m.NextAttemptWhy == "" {
		t.Fatalf("the record does not carry the ask: %+v", m)
	}
	if m.Attempts[0].NextAt == "" {
		t.Error("the attempt was not stamped, so the next one cannot tell a chosen wait from a slow cadence")
	}
	if lbl := objectiveStateLabel(monitorObjective(m)); !strings.Contains(lbl, "waiting:") {
		t.Errorf("the monitor listing would not say what it is waiting for: %q", lbl)
	}
}

// A met goal stops the monitor, so there is no next check to move. An ask made
// on that fire is dropped rather than left on a stopped record.
func TestAMetMonitorGoalDropsThePacingAsk(t *testing.T) {
	db := pinRootDB(t)
	m := EventMonitor{Name: "pr-12", Owner: "craig", Kind: EventKindWatch, Until: "the PR is merged", IntervalSeconds: 900}
	SaveEventMonitor(db, m)

	ask := &pacing.Ask{}
	tool := monitorPacingTool(m, ask)[0]
	if _, err := tool.Handler(t.Context(), map[string]any{"minutes": 240, "why": "checking back later"}); err != nil {
		t.Fatalf("ask: %v", err)
	}

	T := &OrchestrateApp{AppCore: AppCore{LLM: &stubLLM{reply: `{"verdict":"MET","reason":"the PR shows state merged"}`}}}
	T.settleMonitorObjective(t.Context(), m, "PR #12: state changed open → merged", ask)

	cur, _ := GetEventMonitor(db, "craig", "pr-12")
	if !cur.Paused {
		t.Fatal("the met goal did not stop the monitor")
	}
	if !cur.NextAttemptAt.IsZero() || cur.NextAttemptWhy != "" {
		t.Errorf("a stopped monitor carries a pacing ask for a check it will never make: %+v", cur)
	}
}
