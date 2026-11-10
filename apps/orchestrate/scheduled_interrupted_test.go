package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A cut-off fire runs once more only while that still makes sense: not a
// second re-fire, not a paused task, not long after, not outside its window,
// and not just before its next occurrence.
func TestACutOffFireRunsAgainOnlyWhenItStillMakesSense(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	fresh := inflightFire{Started: now.Add(-5 * time.Minute), Payload: orchUpdatePayload{Username: "u"}}
	windowed := fresh
	windowed.Payload.HasWindow, windowed.Payload.WindowFromMin, windowed.Payload.WindowToMin = true, 8*60+30, 9*60
	again := fresh
	again.Refire = true
	stale := fresh
	stale.Started = now.Add(-4 * time.Hour)
	paused := fresh
	paused.Payload.Paused = true
	cases := []struct {
		name string
		m    inflightFire
		next time.Time
		want bool
	}{
		{"fresh, nothing due", fresh, time.Time{}, true},
		{"its next run far off", fresh, now.Add(40 * time.Minute), true},
		{"its next run due shortly", fresh, now.Add(5 * time.Minute), false},
		{"already a second try", again, time.Time{}, false},
		{"started long ago", stale, time.Time{}, false},
		{"paused", paused, time.Time{}, false},
		{"its window closed", windowed, time.Time{}, false},
	}
	for _, c := range cases {
		if got, why := refireDecision(c.m, now, c.next); got != c.want || (!got && why == "") {
			t.Errorf("%s: re-fire %v (%q), want %v", c.name, got, why, c.want)
		}
	}
}

// On start, each mark an earlier process left becomes a note in its agent's
// cortex and, where it makes sense, a queued re-fire; this process's own
// marks are left alone; and a fire's mark is gone once it ends.
func TestAFireCutOffByARestartIsNotedAndRunAgain(t *testing.T) {
	df := newDispatchFixture(t)
	PreInitScheduler()
	t.Cleanup(func() {
		for _, task := range ListScheduledTasks(OrchestrateScheduledRefireKind) {
			UnscheduleTask(task.ID)
		}
	})
	app := df.app
	p := orchUpdatePayload{Username: "u", AgentID: df.agent.ID, Name: "Random engagement", Prompt: "engage"}
	old := func(id string, refire bool) {
		app.DB.Set(inflightTable, id, inflightFire{ID: id, Boot: "an-earlier-process", Payload: p, Started: time.Now().Add(-2 * time.Minute), Refire: refire})
	}
	old("cut-1", false)
	old("cut-2", true)
	clear := app.markInflight(context.Background(), "running-now", p, "")

	app.recoverInterrupted()

	if len(app.DB.Keys(inflightTable)) != 1 {
		t.Errorf("marks left: %v, want only this process's own", app.DB.Keys(inflightTable))
	}
	queued := 0
	for _, task := range ListScheduledTasks(OrchestrateScheduledRefireKind) {
		if strings.Contains(string(task.Payload), df.agent.ID) {
			queued++
		}
	}
	if queued != 1 {
		t.Errorf("%d re-fires queued, want 1 (the second try is not tried a third time)", queued)
	}
	sess, _ := loadChatSession(df.udb, df.agent.ID, cortexSessionID(df.agent.ID))
	var notes []string
	for _, m := range sess.Messages {
		if strings.Contains(m.Content, "cut off when gohort restarted") {
			notes = append(notes, m.Content)
		}
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, "\n"), "It is running once more now.") ||
		!strings.Contains(strings.Join(notes, "\n"), "already a second try") {
		t.Errorf("cortex notes: %q", notes)
	}
	clear()
	if len(app.DB.Keys(inflightTable)) != 0 {
		t.Error("a fire's mark outlived the fire")
	}
}
