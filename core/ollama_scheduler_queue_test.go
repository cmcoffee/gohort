package core

import (
	"context"
	"testing"
	"time"
)

// newQueueTestSched builds a scheduler without touching the package
// singletons, so these tests can't disturb (or be disturbed by) the
// global ollama/llama.cpp schedulers other tests may have started.
func newQueueTestSched(maxParallel int) *OllamaScheduler {
	s := &OllamaScheduler{
		submit:  make(chan *ollamaReqToken, 64),
		release: make(chan string, 64),
		setN:    make(chan int, 4),
		statReq: make(chan chan OllamaSchedStats, 4),
	}
	go s.dispatch(maxParallel)
	return s
}

// pollUntil spins until cond holds or the budget runs out. The
// dispatcher owns its state and is reached only by channel, so there is
// no synchronous way to observe "the token has been queued".
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A second caller must actually wait when the cap is already spent, and
// the dispatcher must report that backlog — this is the state the
// [llm-queue] line prints, so if the numbers are wrong the diagnostic
// misleads rather than informs.
func TestAcquireQueuesBehindTheCap(t *testing.T) {
	s := newQueueTestSched(1)
	ctx := context.Background()

	if err := s.acquire(ctx, "llama.cpp", "first"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	granted := make(chan error, 1)
	go func() { granted <- s.acquire(ctx, "llama.cpp", "second") }()

	pollUntil(t, "the second caller to queue", func() bool {
		st, ok := s.snapshot()
		return ok && st.InFlight == 1 && totalQueued(st) == 1
	})

	select {
	case <-granted:
		t.Fatal("second acquire returned while the only slot was still held")
	case <-time.After(50 * time.Millisecond):
	}

	s.release <- "first"

	select {
	case err := <-granted:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire was never granted after the slot freed")
	}

	pollUntil(t, "the backlog to drain", func() bool {
		st, ok := s.snapshot()
		return ok && totalQueued(st) == 0
	})
}

// A queued caller whose context dies must not strand the slot: the
// dispatcher drops canceled tokens without consuming one, so the next
// caller still gets through.
func TestAcquireReleasesQueueOnCancel(t *testing.T) {
	s := newQueueTestSched(1)

	if err := s.acquire(context.Background(), "llama.cpp", "holder"); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan error, 1)
	go func() { abandoned <- s.acquire(ctx, "llama.cpp", "quitter") }()

	pollUntil(t, "the quitter to queue", func() bool {
		st, ok := s.snapshot()
		return ok && totalQueued(st) == 1
	})
	cancel()

	select {
	case err := <-abandoned:
		if err == nil {
			t.Fatal("canceled acquire reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled acquire never returned")
	}

	s.release <- "holder"

	done := make(chan error, 1)
	go func() { done <- s.acquire(context.Background(), "llama.cpp", "next") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("next acquire: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slot was stranded by the canceled caller")
	}
}

// snapshot sits on the request path, so a dispatcher that never answers
// must cost it a bounded wait rather than the whole turn. Built with no
// dispatch goroutine at all: the buffered send succeeds and the reply
// never arrives.
func TestSnapshotGivesUpWhenDispatcherSilent(t *testing.T) {
	s := &OllamaScheduler{statReq: make(chan chan OllamaSchedStats, 4)}

	done := make(chan bool, 1)
	go func() {
		_, ok := s.snapshot()
		done <- ok
	}()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("snapshot claimed success with no dispatcher running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot hung instead of giving up")
	}
}

func TestTotalQueuedSumsCallers(t *testing.T) {
	got := totalQueued(OllamaSchedStats{Queued: map[string]int{"a": 2, "b": 3}})
	if got != 5 {
		t.Fatalf("totalQueued = %d, want 5", got)
	}
	if got := totalQueued(OllamaSchedStats{}); got != 0 {
		t.Fatalf("totalQueued on empty = %d, want 0", got)
	}
}
