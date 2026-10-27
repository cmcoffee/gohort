package appscript

import (
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func runsDB(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
}

// A live run leaves a record on its app: which script, how it was started,
// how it ended and what it logged, newest first.
func TestALiveRunIsRecordedOnItsApp(t *testing.T) {
	runsDB(t)
	j := Job{Owner: "alice", Slug: "weather", Kind: "data", Name: "forecast", Caller: "alice", Interactive: true}
	heard := 0
	j.OnLog = func(level, msg string) { heard++ }
	trace := Track(&j)
	j.OnLog("info", "fetching 3 cities")
	j.OnLog("warn", "  city 2 timed out  ")
	trace.Done(errors.New("Traceback ... KeyError: 'temp'"))

	if heard != 2 {
		t.Errorf("an OnLog the caller set must still hear every line: heard %d", heard)
	}
	runs := RecentRuns("alice", "weather")
	if len(runs) != 1 {
		t.Fatalf("one run should be recorded, got %d", len(runs))
	}
	r := runs[0]
	if r.Kind != "data" || r.Name != "forecast" || r.Trigger != "page" || r.Other || r.OK || r.Times != 1 || r.At == "" {
		t.Errorf("the record says the wrong thing: %+v", r)
	}
	if !strings.Contains(r.Error, "KeyError") {
		t.Errorf("a failure keeps its error: %q", r.Error)
	}
	if len(r.Log) != 2 || r.Log[0] != "fetching 3 cities" || r.Log[1] != "warn: city 2 timed out" {
		t.Errorf("the log lines are kept, levels marked: %q", r.Log)
	}
	if got := RecentRuns("bob", "weather"); len(got) != 0 {
		t.Errorf("an app's runs are its owner's: %v", got)
	}
	SaveAppSpec(AppSpec{Owner: "alice", Slug: "weather", Name: "Weather"})
	DeleteAppSpec("alice", "weather")
	if got := RecentRuns("alice", "weather"); len(got) != 0 {
		t.Errorf("a deleted app's runs go with it: %v", got)
	}
}

// Another user of a shared app: the owner sees that the run happened and how
// it ended, never what that person's run logged.
func TestAnotherUsersRunKeepsNoLines(t *testing.T) {
	runsDB(t)
	j := Job{Owner: "alice", Slug: "notes", Kind: "action", Name: "save", Caller: "bob", Interactive: true}
	trace := Track(&j)
	j.OnLog("info", "saving bob's private note")
	trace.Done(errors.New("store full"))
	r := RecentRuns("alice", "notes")[0]
	if !r.Other || len(r.Log) != 0 || r.Error != "store full" {
		t.Errorf("another user's run should keep its outcome and no lines: %+v", r)
	}
	// Unattended, nobody asked: a scheduled run.
	j = Job{Owner: "alice", Slug: "notes", Kind: "action", Name: "save", Caller: "alice"}
	Track(&j).Done(nil)
	if r := RecentRuns("alice", "notes")[0]; r.Trigger != "schedule" || r.Other || !r.OK {
		t.Errorf("an unattended owner run reads as scheduled and ok: %+v", r)
	}
}

// One run's lines and error are bounded.
func TestARunsRecordIsBounded(t *testing.T) {
	runsDB(t)
	j := Job{Owner: "alice", Slug: "big", Kind: "data", Name: "loop", Caller: "alice"}
	trace := Track(&j)
	for i := 0; i < runLogLines+5; i++ {
		j.OnLog("info", strings.Repeat("x", runLogWidth+50))
	}
	trace.Done(errors.New(strings.Repeat("a", runErrorTail) + "THE END"))
	r := RecentRuns("alice", "big")[0]
	if len(r.Log) != runLogLines+1 || r.Log[runLogLines] != "(5 more lines not kept)" {
		t.Errorf("lines past the limit are counted, not kept: %d lines, last %q", len(r.Log), r.Log[len(r.Log)-1])
	}
	if n := len([]rune(r.Log[0])); n != runLogWidth+1 {
		t.Errorf("a long line is cut to the width: %d", n)
	}
	if !strings.HasSuffix(r.Error, "THE END") || len([]rune(r.Error)) != runErrorTail+1 {
		t.Errorf("an error keeps its end, where the exception is: %d chars", len([]rune(r.Error)))
	}
}

// Quiet successes of one script fold into a count, so page loads cannot push
// the run that broke out of view; past the newest runs, failures stay longest.
func TestRunsFoldAndFailuresOutlast(t *testing.T) {
	ok := AppRun{Kind: "data", Name: "list", Trigger: "page", OK: true}
	var runs []AppRun
	for i := 0; i < 50; i++ {
		runs = addRun(runs, ok)
	}
	if len(runs) != 1 || runs[0].Times != 50 {
		t.Fatalf("quiet successes of one script should fold into one entry: %d entries, %+v", len(runs), runs[0])
	}
	if folded := addRun(runs, AppRun{Kind: "data", Name: "list", Trigger: "page", OK: true, Log: []string{"x"}}); len(folded) != 2 {
		t.Errorf("a success that logged something is its own entry")
	}
	if folded := addRun(runs, AppRun{Kind: "data", Name: "other", Trigger: "page", OK: true}); len(folded) != 2 {
		t.Errorf("another script's success is its own entry")
	}

	runs = nil
	for i := 0; i < runsFailKeep+5; i++ {
		runs = addRun(runs, AppRun{Kind: "action", Name: "sync", Error: "boom", Log: []string{"try"}})
	}
	for i := 0; i < runsKeep*2; i++ {
		name := "a"
		if i%2 == 1 {
			name = "b" // alternating, so nothing folds
		}
		runs = addRun(runs, AppRun{Kind: "data", Name: name, OK: true})
	}
	if len(runs) != runsKeep+runsFailKeep {
		t.Fatalf("kept %d runs, want %d newest + %d failures", len(runs), runsKeep, runsFailKeep)
	}
	for _, r := range runs[runsKeep:] {
		if r.OK {
			t.Fatalf("past the newest runs only failures are kept: %+v", r)
		}
	}
}
