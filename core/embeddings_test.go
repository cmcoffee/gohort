package core

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// embedServer counts concurrent in-flight requests and reports the peak.
func embedServer(t *testing.T, hold time.Duration) (url string, peak *int32) {
	t.Helper()
	var inFlight, high int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&high)
			if n <= old || atomic.CompareAndSwapInt32(&high, old, n) {
				break
			}
		}
		time.Sleep(hold)
		atomic.AddInt32(&inFlight, -1)
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &high
}

// Embeddings took no scheduler slot at all. They are usually served by the same
// process as the worker model, so an unscheduled embed competes with chat for
// the very resource the queue exists to protect — and on stock llama.cpp, which
// is single-threaded, an embed arriving mid-generation causes 503s rather than
// merely being unfair.
//
// With the llama.cpp scheduler capped at 1, concurrent embeds must serialize.
func TestEmbedsRespectTheLocalModelQueue(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)
	StartLlamacppScheduler(1)
	t.Cleanup(func() { StartLlamacppScheduler(0) })

	cfg := EmbeddingConfig{Enabled: true, Endpoint: url, Model: "m"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EmbedWith(t.Context(), cfg, "text"); err != nil {
				t.Errorf("embed: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(peak); got > 1 {
		t.Errorf("peak concurrent embeds = %d with the queue capped at 1 — embeds are bypassing the scheduler", got)
	}
}

// An embed bound for a PEER must NOT take a local slot. The work happens on
// another instance, which queues it against its own load; holding a local slot
// would stall it behind a model that is not doing the work.
func TestPeerBoundEmbedsDoNotTakeALocalSlot(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)
	StartLlamacppScheduler(1)
	t.Cleanup(func() { StartLlamacppScheduler(0) })

	// The marker the exclusion keys on is the peer path.
	cfg := EmbeddingConfig{Enabled: true, Endpoint: url + "/api/peer/v1", Model: "m"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EmbedWith(t.Context(), cfg, "text"); err != nil {
				t.Errorf("embed: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(peak); got < 2 {
		t.Errorf("peak concurrent peer-bound embeds = %d — they are being queued locally, "+
			"which stalls remote work behind a local model that is not doing it", got)
	}
}

// With no scheduler configured, nothing changes: embeds pass straight through,
// exactly as before. The queue is opt-in and its absence must not serialize a
// deployment that never asked for it.
func TestEmbedsPassThroughWithNoSchedulerConfigured(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)

	cfg := EmbeddingConfig{Enabled: true, Endpoint: url, Model: "m"}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EmbedWith(t.Context(), cfg, "text"); err != nil {
				t.Errorf("embed: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(peak); got < 2 {
		t.Errorf("peak concurrent embeds = %d with no scheduler running — an unconfigured "+
			"deployment is being serialized", got)
	}
}

// The caller label is what fair-share has to be fair BETWEEN. Unlabeled, every
// embed looks like one caller and round-robin has nothing to alternate — a peer
// running bulk ingestion would be indistinguishable from the local turn it is
// delaying.
func TestEmbedCallerLabelling(t *testing.T) {
	if got := embedCaller(t.Context()); got != embedCallerLocal {
		t.Errorf("unlabelled caller = %q, want %q", got, embedCallerLocal)
	}
	ctx := WithEmbedCaller(t.Context(), "peer:mac")
	if got := embedCaller(ctx); got != "peer:mac" {
		t.Errorf("labelled caller = %q", got)
	}
	// A blank label must not erase the default into an empty caller id, which
	// the scheduler would treat as its own distinct queue.
	if got := embedCaller(WithEmbedCaller(t.Context(), "   ")); got != embedCallerLocal {
		t.Errorf("blank label = %q, want %q", got, embedCallerLocal)
	}
}
