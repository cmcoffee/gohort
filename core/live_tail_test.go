package core

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

// Tail shows a viewer everything so far, then each new event, and reports
// whether the run finished or the viewer left first.
func TestTailReplaysThenFollowsToTheEnd(t *testing.T) {
	m := NewLiveSessionMap[string](0)
	m.Register("run", "label", func() {})
	m.AppendEvent("run", "one", false)
	m.AppendEvent("run", "two", false)

	go func() {
		time.Sleep(50 * time.Millisecond)
		m.AppendEvent("run", "three", true)
	}()
	sse, _ := NewSSEWriter(httptest.NewRecorder())
	var got []string
	done := m.Tail(context.Background(), sse, "run", func(ev string) error {
		got = append(got, ev)
		return nil
	})
	if !done {
		t.Fatal("the run finished; Tail must say so")
	}
	if len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Errorf("events = %v, want the two buffered then the one that arrived", got)
	}
}

// A viewer leaving stops the tail, not the run.
func TestTailStopsWhenTheViewerLeaves(t *testing.T) {
	m := NewLiveSessionMap[string](0)
	m.Register("run", "label", func() {})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	sse, _ := NewSSEWriter(httptest.NewRecorder())
	start := time.Now()
	if m.Tail(ctx, sse, "run", func(string) error { return nil }) {
		t.Error("the viewer left before the run finished; Tail must not report it done")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Tail kept polling after the viewer left")
	}
	if events, done := m.SnapshotEvents("run"); events == nil || done {
		t.Error("the run itself must be untouched by a viewer leaving")
	}
}

// A run started for an existing record keeps that record's id, so a page
// opening the record can rejoin it.
func TestAPipelineCanRunUnderAGivenID(t *testing.T) {
	if id := pipelineID(PipelineConfig{ID: "record-42"}); id != "record-42" {
		t.Errorf("id = %q, want the record's own", id)
	}
	if a, b := pipelineID(PipelineConfig{}), pipelineID(PipelineConfig{}); a == "" || a == b {
		t.Errorf("without an id, each run gets a fresh one: %q %q", a, b)
	}
}
