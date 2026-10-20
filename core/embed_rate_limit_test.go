package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// rateLimitedEmbedder answers 429 with Retry-After for its first `refuse`
// calls, then embeds.
func rateLimitedEmbedder(t *testing.T, refuse int32, retryAfter string) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := atomic.AddInt32(&calls, 1); n <= refuse {
			w.Header().Set("Retry-After", retryAfter)
			http.Error(w, `{"error":"rate limit reached for this key"}`, http.StatusTooManyRequests)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{0.1, 0.2}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	prev := GetEmbeddingConfig()
	SetEmbeddingConfig(EmbeddingConfig{Endpoint: srv.URL, Model: "m", Enabled: true})
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	return srv, &calls
}

// A repair pass against an embedder that rate limits it (a peer key's per
// minute ceiling) waits out each Retry-After and repairs everything: the
// refusals are not failures, so the breaker that ends a pass against a dead
// endpoint never trips.
func TestARateLimitingEmbedderIsWaitedOut(t *testing.T) {
	_, calls := rateLimitedEmbedder(t, 2, "1")
	db := &DBase{Store: kvlite.MemStore()}
	ids := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, id := range ids {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "unembedded " + id})
	}
	started := time.Now()
	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != len(ids) {
		t.Fatalf("repaired %d of %d (calls %d)", fixed, len(ids), *calls)
	}
	if took := time.Since(started); took < 2*time.Second {
		t.Errorf("finished in %s: the Retry-After pauses were not waited out", took)
	}
	// One batch for all seven, sent three times: refused twice, then answered.
	if got := int(*calls); got != 3 {
		t.Errorf("%d calls, want the one batch twice refused and once answered", got)
	}
}

// A backend that answers one vector however many inputs it is sent is not
// one to batch with: the pass sees the short answer, goes one chunk a request
// for the rest of the pass, and repairs everything.
func TestABackendThatEmbedsOneAtATimeStillRepairsEverything(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.3, 0.4}}})
	}))
	t.Cleanup(srv.Close)
	prev := GetEmbeddingConfig()
	SetEmbeddingConfig(EmbeddingConfig{Endpoint: srv.URL, Model: "m", Enabled: true})
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	db := &DBase{Store: kvlite.MemStore()}
	for _, id := range []string{"a", "b", "c", "d"} {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "unembedded " + id})
	}
	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != 4 {
		t.Fatalf("repaired %d of 4", fixed)
	}
	if got := atomic.LoadInt32(&calls); got != 5 {
		t.Errorf("%d calls, want the batch and then one per chunk", got)
	}
}

// A pass of more chunks than one batch holds sends them reembedBatchSize to a
// request.
func TestAPassSendsChunksInBatches(t *testing.T) {
	var sizes []int
	_, calls := stubEmbedServerOf(t, "m", func(_ int32, inputs []string) (int, []float32) {
		sizes = append(sizes, len(inputs))
		return 200, []float32{1, 0}
	})
	db := &DBase{Store: kvlite.MemStore()}
	n := reembedBatchSize*2 + 3
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%03d", i)
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "unembedded " + id})
	}
	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != n {
		t.Fatalf("repaired %d of %d", fixed, n)
	}
	if *calls != 3 || sizes[0] != reembedBatchSize || sizes[2] != 3 {
		t.Errorf("%d requests of %v, want two full batches and the rest", *calls, sizes)
	}
}

// An embed with a deadline shorter than the pause fails at once without
// sending: a turn's query embed does not add to the refusals.
func TestAnEmbedThatCannotWaitDoesNotSend(t *testing.T) {
	srv, calls := rateLimitedEmbedder(t, 1, "30")
	cfg := GetEmbeddingConfig()
	if _, err := EmbedWith(context.Background(), cfg, "first"); err == nil {
		t.Fatal("the refused call reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := EmbedWith(ctx, cfg, "second")
	if err == nil || !strings.Contains(err.Error(), "rate limited") || atomic.LoadInt32(calls) != 1 {
		t.Fatalf("err %v after %d call(s)", err, *calls)
	}
	if d := embedPausedFor(embedEndpointURL(cfg)); d < 25*time.Second || d > 30*time.Second {
		t.Errorf("paused for %s, want the 30s Retry-After", d)
	}
	_ = srv
}

// A refused peer is told when the key's minute frees up, not a flat minute.
func TestAPeerIsToldWhenTheWindowFreesUp(t *testing.T) {
	k := PeerKey{ID: "k-retry"}
	peerRateMu.Lock()
	peerRate[k.ID] = &peerWindow{start: time.Now().Add(-50 * time.Second), n: 600}
	peerRateMu.Unlock()
	t.Cleanup(func() { peerRateMu.Lock(); delete(peerRate, k.ID); peerRateMu.Unlock() })
	if got := peerRetryAfterHeader(peerRateRetryAfter(k)); got != "10" && got != "11" {
		t.Errorf("Retry-After %q, want about 10 seconds", got)
	}
	if got := peerRetryAfterHeader(peerRateRetryAfter(PeerKey{ID: "unseen"})); got != "1" {
		t.Errorf("a key with no window: %q", got)
	}
}
