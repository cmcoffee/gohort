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

// Each event reaches a viewer as it is appended. Tail used to check every
// 500ms and send what had piled up, so a pipeline's streamed text arrived in
// half-second clumps for every viewer, first or reconnected.
func TestTailSendsEachEventAsItArrives(t *testing.T) {
	// Each event is the moment it was appended.
	m := NewLiveSessionMap[time.Time](0)
	m.Register("run", "label", func() {})
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(30 * time.Millisecond)
			m.AppendEvent("run", time.Now(), i == 4)
		}
	}()
	sse, _ := NewSSEWriter(httptest.NewRecorder())
	var lags []time.Duration
	m.Tail(context.Background(), sse, "run", func(at time.Time) error {
		lags = append(lags, time.Since(at))
		return nil
	})
	for _, lag := range lags {
		if lag > 100*time.Millisecond {
			t.Errorf("an event reached the viewer %v after it was appended", lag)
		}
	}
	if len(lags) != 5 {
		t.Errorf("viewer got %d events, want 5", len(lags))
	}
}

// A session cleaned up while a viewer tails it ends the tail at once rather
// than at the next check.
func TestTailEndsWhenTheSessionIsRemoved(t *testing.T) {
	m := NewLiveSessionMap[string](0)
	m.Register("run", "label", func() {})
	go func() {
		time.Sleep(30 * time.Millisecond)
		m.ScheduleCleanupAfter("run", 0)
	}()
	sse, _ := NewSSEWriter(httptest.NewRecorder())
	start := time.Now()
	if !m.Tail(context.Background(), sse, "run", func(string) error { return nil }) {
		t.Error("a removed session reads as finished")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("Tail took %v to notice the session was gone", time.Since(start))
	}
}
