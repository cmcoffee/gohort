package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

// servedEmbeds serves one embeddings request from a peer key and returns the
// response, with the embedder answering through a fake service that records
// the size of every request it was sent. short answers a batch of more than
// one text with one vector too few, as a backend that will not take a batch
// does.
func servedEmbeds(t *testing.T, input string, short bool) (*httptest.ResponseRecorder, []int) {
	t.Helper()
	peerTestDB(t)
	savedEmbed, savedCfg := media.EmbedService, GetEmbeddingConfig()
	t.Cleanup(func() { media.EmbedService = savedEmbed; SetEmbeddingConfig(savedCfg) })
	var mu sync.Mutex
	var sizes []int
	media.EmbedService = func(_ context.Context, _, _ string, texts []string) ([][]float32, error) {
		mu.Lock()
		sizes = append(sizes, len(texts))
		mu.Unlock()
		out := make([][]float32, len(texts))
		for i, text := range texts {
			out[i] = []float32{float32(len(text)), 1}
		}
		if short && len(out) > 1 {
			out = out[:len(out)-1]
		}
		return out, nil
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec", Model: "m1"})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings", strings.NewReader(input))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerEmbeddings(w, r)
	mu.Lock()
	defer mu.Unlock()
	return w, append([]int(nil), sizes...)
}

// servedVectorLens reads the response's vectors back as the first component
// of each, which the fake sets to the length of the text it was made from, so
// a vector out of order or in the wrong slot shows.
func servedVectorLens(t *testing.T, w *httptest.ResponseRecorder) []int {
	t.Helper()
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	var lens []int
	for i, d := range out.Data {
		if d.Index != i {
			t.Errorf("data[%d] has index %d", i, d.Index)
		}
		lens = append(lens, int(d.Embedding[0]))
	}
	return lens
}

// A peer's batch reaches the embedder as ONE request. Sent one text at a
// time, a batch of 32 waited its turn in the local embed queue 32 times
// behind this instance's own embeds, and a peer request took 6-7s where the
// same batch from a local pass took 1s.
func TestPeerEmbeddingsSendsABatchAsOneRequest(t *testing.T) {
	w, sizes := servedEmbeds(t, `{"input":["a","bb","ccc"]}`, false)
	if w.Code != http.StatusOK {
		t.Fatalf("batch → %d: %s", w.Code, w.Body.String())
	}
	if len(sizes) != 1 || sizes[0] != 3 {
		t.Errorf("the embedder was sent %v, want one request of 3", sizes)
	}
	if got := servedVectorLens(t, w); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("vectors came back as %v, want one per text in order [1 2 3]", got)
	}
}

// A backend that will not take the batch (it answers short) is tried one
// text at a time, as the re-embed pass does, and the peer still gets every
// vector in order rather than an error.
func TestPeerEmbeddingsFallsBackToOneAtATime(t *testing.T) {
	w, sizes := servedEmbeds(t, `{"input":["a","bb","ccc"]}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("short batch → %d: %s", w.Code, w.Body.String())
	}
	if len(sizes) != 4 || sizes[0] != 3 || sizes[1] != 1 || sizes[2] != 1 || sizes[3] != 1 {
		t.Errorf("the embedder was sent %v, want the batch of 3 and then 3 of 1", sizes)
	}
	if got := servedVectorLens(t, w); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("vectors came back as %v, want one per text in order [1 2 3]", got)
	}
}

// A peer's request reaches the work done for it marked as the peer's
// (PeerRequestOf), set at the gate every peer handler passes through, so a
// line that would log its content logs its size. This instance's own call
// carries no mark.
func TestAPeersRequestIsMarkedAsTheirs(t *testing.T) {
	peerTestDB(t)
	savedEmbed, savedCfg := media.EmbedService, GetEmbeddingConfig()
	t.Cleanup(func() { media.EmbedService = savedEmbed; SetEmbeddingConfig(savedCfg) })
	var marks []string
	media.EmbedService = func(ctx context.Context, _, _ string, texts []string) ([][]float32, error) {
		marks = append(marks, PeerRequestOf(ctx))
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1}
		}
		return out, nil
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec", Model: "m1"})
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/embeddings", strings.NewReader(`{"input":["private words"]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	HandlePeerEmbeddings(httptest.NewRecorder(), r)
	if _, err := embedTexts(context.Background(), GetEmbeddingConfig(), []string{"my own"}); err != nil {
		t.Fatal(err)
	}
	if len(marks) != 2 || marks[0] != "mac" || marks[1] != "" {
		t.Errorf("marks = %q, want the peer's call marked mac and this instance's unmarked", marks)
	}
}
