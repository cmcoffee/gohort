package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/jsonapi"
)

// A run started with withRun keeps what it does as it does it: the model's
// words for a round, each call from start (running, how long so far) to end
// (its result's first line, or its error), and its own progress lines, one
// per stretch rather than one per tick. It stays bounded, and says so.
func TestARunKeepsItsStepsAsItGoes(t *testing.T) {
	reg := NewRunRegistry()
	run := reg.Create("u", "a1", "", nil).Describe("scheduled", "Writer", "the morning post")
	ctx := withRun(context.Background(), run)
	if parentRunFromCtx(ctx) != run.ID {
		t.Fatal("withRun must still hand the run's id down for its sub-agents to nest under")
	}

	run.RoundStarted(1, "Checking the feed first.\nThen writing.")
	done := run.ToolStarted("fetch_feed", map[string]any{"url": "https://example.test/feed"})
	run.SetDetail("1 of 3 items")
	run.SetDetail("2 of 3 items")
	failing := run.ToolStarted("post", map[string]any{"title": "x"})
	slow := run.ToolStarted("render", nil)
	done("12 items\nmore", nil)
	failing("", errors.New("the blog said 503"))

	steps, dropped := run.Trace()
	if dropped != 0 || len(steps) != 5 {
		t.Fatalf("steps %d, dropped %d: %+v", len(steps), dropped, steps)
	}
	if s := steps[0]; s.Kind != traceSaid || s.Text != "Checking the feed first." || s.Round != 1 {
		t.Errorf("what the model said, to its first line: %+v", s)
	}
	if s := steps[1]; s.Name != "fetch_feed" || !strings.Contains(s.Text, "example.test/feed") || s.Result != "12 items" || s.Ended.IsZero() || s.Round != 1 {
		t.Errorf("a finished call: %+v", s)
	}
	if s := steps[2]; s.Kind != traceNote || s.Text != "2 of 3 items" {
		t.Errorf("a run's ticks are one line, its latest: %+v", s)
	}
	if s := steps[3]; s.Err != "the blog said 503" {
		t.Errorf("a failed call: %+v", s)
	}
	if s := steps[4]; !s.Ended.IsZero() {
		t.Errorf("a call still going has no end: %+v", s)
	}

	rows := traceRows(reg, reg.Activity("u"), steps[4].At)
	if rows[0].Step != "Writer" || rows[0].State != RunStatusRunning || rows[0].Detail != "the morning post" {
		t.Errorf("the run heads its trace: %+v", rows[0])
	}
	if r := rows[1]; r.Step != "round 1 · render" || r.State != "running" || r.Time == "" {
		t.Errorf("newest first, a running call with its time so far: %+v", r)
	}
	if r := rows[2]; r.State != "failed" || r.Result != "the blog said 503" || !strings.HasPrefix(r.Time, "took ") {
		t.Errorf("a failed call shows its error: %+v", r)
	}

	slow("ok", nil)
	run.Complete(RunStatusCompleted)
	run.ToolStarted("late", nil)
	run.SetDetail("after the end")
	if after, _ := run.Trace(); len(after) != 5 {
		t.Errorf("a finished run takes no more steps: %d", len(after))
	}

	long := reg.Create("u", "a2", "", nil)
	for i := 0; i < runTraceMax+7; i++ {
		long.ToolStarted("step", nil)("", nil)
	}
	steps, dropped = long.Trace()
	if len(steps) != runTraceMax || dropped != 7 {
		t.Errorf("bounded at %d with the count dropped: %d kept, %d dropped", runTraceMax, len(steps), dropped)
	}
	rows = traceRows(reg, descendantsOf(reg.Activity("u"), long.ID), steps[0].At)
	if last := rows[len(rows)-1]; !strings.Contains(last.Step, "7 earlier steps") {
		t.Errorf("the view says the list starts part way in: %+v", last)
	}
}

// The trace view shows a run and its sub-agents' steps, each marked with the
// sub-agent's name, to the run's owner only: anyone else, or a run that is
// gone, gets nothing rather than an error that says there was something.
func TestTheTraceViewIsTheOwnersAndHoldsItsSubAgents(t *testing.T) {
	T := &OrchestrateApp{}
	reg := T.runsRegistry()
	root := reg.Create("u", "lead", "", nil).Describe("chat", "Lead", "plan the trip")
	child := reg.Create("u", "scout", "", nil).Describe("dispatch", "Scout", "find flights").Parent(root.ID)
	root.ToolStarted("delegate", map[string]any{"to": "scout"})
	child.ToolStarted("web_search", map[string]any{"q": "flights"})("3 results", nil)

	get := func(user, id string) []consoleTraceRow {
		out, err := T.handleConsoleTrace(&jsonapi.Call{W: httptest.NewRecorder(),
			R: httptest.NewRequest(http.MethodGet, "/api/console/trace?id="+id, nil), User: user})
		if err != nil {
			t.Fatalf("trace: %v", err)
		}
		return out.([]consoleTraceRow)
	}
	rows := get("u", root.ID)
	var steps []string
	for _, r := range rows {
		steps = append(steps, r.Step)
	}
	if strings.Join(steps, "|") != "Lead|↳ Scout · web_search|delegate" {
		t.Errorf("the run, then its steps and its sub-agent's, newest first: %q", steps)
	}
	if n := len(get("someone-else", root.ID)); n != 0 {
		t.Errorf("another user sees %d rows of this run", n)
	}
	if n := len(get("u", "no-such-run")); n != 0 {
		t.Errorf("a run that is gone shows %d rows", n)
	}
}
