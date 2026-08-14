package orchestrate

// A tool that runs for minutes is indistinguishable from a hung one: the
// last thing anyone sees is that the call started. The wrapper is the
// only place that knows a call is in flight, so the heartbeat lives
// there and covers every tool rather than the one that was slow today.

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSSE captures what the turn emitted.
type recordingSSE struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingSSE) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, s)
}

func (r *recordingSSE) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func TestSlowToolEmitsAHeartbeat(t *testing.T) {
	if toolHeartbeat > 20*time.Second {
		t.Fatalf("heartbeat of %s is too long to answer \"is it stuck?\"", toolHeartbeat)
	}
	// The mechanism, exercised at test speed: the wrapper's goroutine
	// shape is what is under test, not the constant.
	rec := &recordingSSE{}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		began := time.Now()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				rec.note("search_bundles — still running (" + time.Since(began).Round(time.Millisecond).String() + ")")
			}
		}
	}()
	time.Sleep(45 * time.Millisecond)
	close(done)
	wg.Wait()

	got := rec.all()
	if len(got) < 2 {
		t.Fatalf("a call running several intervals should beat more than once, got %d", len(got))
	}
	if !strings.Contains(got[0], "still running") || !strings.Contains(got[0], "search_bundles") {
		t.Errorf("a heartbeat should name the tool and say it is alive: %q", got[0])
	}
	// And it must STOP when the call returns — a ticker left running
	// after the result would keep claiming work that finished.
	before := len(rec.all())
	time.Sleep(30 * time.Millisecond)
	if after := len(rec.all()); after != before {
		t.Errorf("heartbeat kept firing after the call ended: %d → %d", before, after)
	}
}

// The wrapper must not emit for tools whose chips are suppressed, and
// must not leak a goroutine per call.
func TestHeartbeatIsWiredIntoTheWrapper(t *testing.T) {
	src := readSourceFile(t, "runner.go")
	if !strings.Contains(src, "close(stopBeat)") {
		t.Error("the heartbeat is never stopped — one goroutine per tool call would leak")
	}
	if !strings.Contains(src, "if !hidden {\n\t\t\t\tgo func(label string) {") {
		t.Error("the heartbeat should be skipped for tools whose chips are hidden")
	}
	// It must start BEFORE the handler runs, or it can never fire during
	// the call it is reporting on.
	beat := strings.Index(src, "stopBeat := make(chan struct{})")
	call := strings.Index(src, "out, err := orig(args)\n\t\t\tclose(stopBeat)")
	if beat < 0 || call < 0 || beat > call {
		t.Error("the heartbeat must be armed before the handler is called")
	}
}
