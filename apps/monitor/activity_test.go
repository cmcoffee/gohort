package monitor

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// One table, running first: the viewer's agent runs, then the rest of the
// live feed; then everything finished, newest first, agent runs and apps
// together. A run of the viewer's that is in both feeds is listed once, from
// the ledger, which has its trace and its Cancel.
func TestRunningFirstThenFinishedNewestFirst(t *testing.T) {
	now := time.Now()
	agents := []orchestrate.ActivityRow{
		{Agent: "helper", ID: "run-1", Running: true, Cancellable: true, Activity: "● running: 12s"},
		{Agent: "↳ sub", ID: "run-2", Running: true},
		{Agent: "old", ID: "run-0", Activity: "completed 5m0s ago", Ended: now.Add(-5 * time.Minute)},
	}
	entries := []LiveEntry{
		{ID: "run-1", App: "helper", URL: runLinkPrefix + "run-1", Owner: "me"},
		{ID: "run-9", App: "someone's agent", URL: runLinkPrefix + "run-9", Owner: "other", Order: 100},
		{ID: "job-1", App: "Research", Path: "/research", Label: "a question", Owner: "other"},
	}
	ended := []seenWork{{entry: LiveEntry{ID: "job-0", App: "Debate", Path: "/debate"}, since: now.Add(-4 * time.Minute), ended: now.Add(-time.Minute)}}

	var T MonitorApp
	rows := T.rows(httptest.NewRequest("GET", "/monitor/api/activity", nil), agents, entries, ended, now)

	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	want := []string{"run-1", "run-2", "job-1", "run-9", "job-0", "run-0"}
	if len(ids) != len(want) {
		t.Fatalf("rows %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("rows %v, want %v", ids, want)
		}
	}
	byID := map[string]monitorRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID["run-1"]; !r.Run || !r.Cancellable {
		t.Errorf("the viewer's run keeps its Trace and Cancel: %+v", r)
	}
	if r := byID["run-9"]; r.Run || r.Cancellable || r.Href != "" || r.Via != "agent" {
		t.Errorf("someone else's run is listed, not opened: %+v", r)
	}
	if r := byID["job-1"]; r.Doing == "a question" {
		t.Errorf("someone else's app work shows without its details: %q", r.Doing)
	}
	if r := byID["job-0"]; r.Running || r.Status != "ended 1m00s ago: took about 3m00s" {
		t.Errorf("finished app work says when it ended: %+v", r)
	}
}

// The live feed forgets an app's work the moment it finishes; the Monitor
// keeps it for a while, but only work with a page to open, not a peer
// borrowing the model or something that was only ever queued, and never an
// agent run, which the ledger keeps with how it ended.
func TestFinishedWorkIsKeptForAWhile(t *testing.T) {
	var l workLog
	t0 := time.Now()
	l.observe([]LiveEntry{
		{ID: "job", App: "Research", Path: "/research"},
		{ID: "peerwork:x", App: "Peers"},
		{ID: "waiting", App: "Research", Path: "/research", Queued: true},
		{ID: "run", URL: runLinkPrefix + "run"},
	}, t0)
	if got := l.since("job", t0.Add(time.Minute)); !got.Equal(t0) {
		t.Errorf("running work is timed from when it was first seen: %v", got)
	}
	l.observe(nil, t0.Add(time.Minute))
	kept := l.finished()
	if len(kept) != 1 || kept[0].entry.ID != "job" || !kept[0].ended.Equal(t0.Add(time.Minute)) {
		t.Fatalf("kept %+v, want only the app's work", kept)
	}
	// The same work running again is not finished.
	l.observe([]LiveEntry{{ID: "job", App: "Research", Path: "/research"}}, t0.Add(2*time.Minute))
	if len(l.finished()) != 0 {
		t.Error("work running again is listed as finished too")
	}
	l.observe(nil, t0.Add(3*time.Minute))
	l.observe(nil, t0.Add(3*time.Minute+keepFor))
	if len(l.finished()) != 0 {
		t.Error("finished work stays past keepFor")
	}
}

// At most keepMost finished entries are kept, the newest.
func TestFinishedWorkIsCapped(t *testing.T) {
	var l workLog
	t0 := time.Now()
	for i := 0; i < keepMost+5; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		l.observe([]LiveEntry{{ID: "job" + string(rune('a'+i)), Path: "/research"}}, at)
		l.observe(nil, at.Add(time.Millisecond))
	}
	kept := l.finished()
	if len(kept) != keepMost {
		t.Fatalf("kept %d, want %d", len(kept), keepMost)
	}
	if kept[0].entry.ID != "job"+string(rune('a'+keepMost+4)) {
		t.Errorf("newest first: %s", kept[0].entry.ID)
	}
}
