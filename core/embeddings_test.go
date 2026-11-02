package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/media"
)

// useConnectorEmbedder points embeddings at a service connector at endpoint,
// standing in for core/service (which imports this package): it posts the
// texts to endpoint/embeddings as the connector the migration writes does,
// the model named when there is one, reads the vectors from data[].embedding,
// embeddings or embedding, and pauses the host on a 429. Its address is
// endpoint, so one on this machine queues behind the local model.
func useConnectorEmbedder(t *testing.T, endpoint, model string) EmbeddingConfig {
	t.Helper()
	savedEmbed, savedAddr, savedCfg := media.EmbedService, media.ServiceAddress, GetEmbeddingConfig()
	t.Cleanup(func() {
		media.EmbedService, media.ServiceAddress = savedEmbed, savedAddr
		SetEmbeddingConfig(savedCfg)
	})
	media.ServiceAddress = func(string) string { return endpoint }
	media.EmbedService = func(ctx context.Context, _, model string, texts []string) ([][]float32, error) {
		if err := media.WaitPause(ctx, endpoint); err != nil {
			return nil, err
		}
		body := map[string]any{"input": texts}
		if model != "" {
			body["model"] = model
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/embeddings", bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			media.NotePause(endpoint, resp.Header.Get("Retry-After"))
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
		}
		var out struct {
			Data       []struct{ Embedding []float32 } `json:"data"`
			Embeddings [][]float32                     `json:"embeddings"`
			Embedding  []float32                       `json:"embedding"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		switch {
		case len(out.Data) > 0:
			var vecs [][]float32
			for _, d := range out.Data {
				vecs = append(vecs, d.Embedding)
			}
			return vecs, nil
		case len(out.Embeddings) > 0:
			return out.Embeddings, nil
		case len(out.Embedding) > 0:
			return [][]float32{out.Embedding}, nil
		}
		return nil, nil
	}
	// The endpoint stored is the connector's name, as picking one in the
	// settings stores it: its address is only the connector's.
	cfg := EmbeddingConfig{Enabled: true, Provider: "test", Endpoint: "test", Model: model}
	SetEmbeddingConfig(cfg)
	return cfg
}

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
// With the llama.cpp scheduler capped at 1, concurrent embeds through a
// connector on this machine must serialize.
func TestEmbedsRespectTheLocalModelQueue(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)
	StartLlamacppScheduler(1)
	t.Cleanup(func() { StartLlamacppScheduler(0) })

	cfg := useConnectorEmbedder(t, url, "m")
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

// An embed bound for a PEER or a hosted service must NOT take a local slot.
// The work happens elsewhere, which queues it against its own load; holding a
// local slot would stall it behind a model that is not doing the work.
func TestRemoteEmbedsDoNotTakeALocalSlot(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)
	StartLlamacppScheduler(1)
	t.Cleanup(func() { StartLlamacppScheduler(0) })

	hosted := useConnectorEmbedder(t, url, "m")
	media.ServiceAddress = func(string) string { return "https://embeddings.example.com/v1" }
	peer := EmbeddingConfig{Enabled: true, Provider: "peer:elsewhere", Endpoint: url + "/api/peer/v1", Model: "m"}
	for _, cfg := range []EmbeddingConfig{hosted, peer} {
		atomic.StoreInt32(peak, 0)
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
			t.Errorf("%s: peak concurrent embeds = %d: they are being queued locally, "+
				"which stalls remote work behind a local model that is not doing it", cfg.Provider, got)
		}
	}
}

// Embeddings are made by a connector or a peer, and nothing else: a config
// naming neither (one stored before the migration ran) is refused, not sent
// to its endpoint.
func TestAnEmbedderThatIsNeitherConnectorNorPeerIsRefused(t *testing.T) {
	url, _ := embedServer(t, 0)
	for _, p := range []string{"", EmbeddingProviderLocal} {
		_, err := EmbedWith(t.Context(), EmbeddingConfig{Enabled: true, Provider: p, Endpoint: url, Model: "m"}, "text")
		if err == nil || !strings.Contains(err.Error(), "not set to a connector or a peer") {
			t.Errorf("provider %q: %v", p, err)
		}
	}
	if _, err := ResolveEmbeddingProvider(EmbeddingConfig{Enabled: true, Provider: EmbeddingProviderLocal}); err == nil {
		t.Error("saving embeddings on this instance's own endpoint was accepted")
	}
	if _, err := ResolveEmbeddingProvider(EmbeddingConfig{Provider: EmbeddingProviderLocal}); err != nil {
		t.Errorf("turning embeddings off was refused: %v", err)
	}
}

// With no scheduler configured, nothing changes: embeds pass straight through,
// exactly as before. The queue is opt-in and its absence must not serialize a
// deployment that never asked for it.
func TestEmbedsPassThroughWithNoSchedulerConfigured(t *testing.T) {
	url, peak := embedServer(t, 40*time.Millisecond)

	cfg := useConnectorEmbedder(t, url, "m")
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

// Embeddings set to a service go through media.EmbedService, in a batch, with the
// configured model; a batch answered short is refused, and with no service
// package installed the config is refused rather than sent anywhere.
func TestEmbedsThroughAServiceAreCounted(t *testing.T) {
	saved := media.EmbedService
	t.Cleanup(func() { media.EmbedService = saved })
	var gotName, gotModel string
	drop := false
	media.EmbedService = func(_ context.Context, name, model string, texts []string) ([][]float32, error) {
		gotName, gotModel = name, model
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{float32(i), 1}
		}
		if drop {
			out = out[:len(out)-1]
		}
		return out, nil
	}
	cfg := EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec", Model: "m1"}
	vecs, err := embedTexts(context.Background(), cfg, []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[1][0] != 1 || gotName != "vec" || gotModel != "m1" {
		t.Fatalf("batch: %v %v (%s, %s)", vecs, err, gotName, gotModel)
	}
	drop = true
	if _, err := embedTexts(context.Background(), cfg, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "1 vector(s) for 2") {
		t.Errorf("a short batch: %v", err)
	}
	media.EmbedService = nil
	if _, err := embedTexts(context.Background(), cfg, []string{"a"}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("no service package: %v", err)
	}
}

// A service is its own embedding space: picking one names it as the
// endpoint (so EmbedVersion changes) and drops a key typed for another
// embedder, and one that declares no model is stamped with its name, since a
// blank stamp would be compared with every stored row.
func TestAServiceEmbedderIsItsOwnSpace(t *testing.T) {
	saved := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(saved) })
	SetEmbeddingConfig(EmbeddingConfig{})
	cfg, err := ResolveEmbeddingProvider(EmbeddingConfig{Provider: "vec", Endpoint: "http://localhost:11434/api", APIKey: "local-key", Model: "nomic-embed-text"})
	if err != nil || cfg.Endpoint != "vec" || cfg.APIKey != "" {
		t.Fatalf("resolved: %+v %v", cfg, err)
	}
	if got := (EmbeddingConfig{Provider: "vec"}).spaceStamp(); got != "service:vec" {
		t.Errorf("a model-less service is stamped %q", got)
	}
	if got := (EmbeddingConfig{Provider: "vec", Model: "m1", DocPrefix: "passage: "}).spaceStamp(); got != "m1+doc:passage: " {
		t.Errorf("a service with a model is stamped %q", got)
	}
	if got := (EmbeddingConfig{Provider: "local"}).spaceStamp(); got != "" {
		t.Errorf("a model-less local embedder stays unstamped, got %q", got)
	}
	// A connector made from the settings this instance embedded with before
	// connectors keeps the endpoint embeddings were made at, and with it
	// their space: saved again it keeps it, and with no model it stays
	// unstamped, as the rows it made are.
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "embeddings", Endpoint: "http://localhost:8080/v1"})
	again, err := ResolveEmbeddingProvider(EmbeddingConfig{Enabled: true, Provider: "embeddings"})
	if err != nil || again.Endpoint != "http://localhost:8080/v1" {
		t.Errorf("the connector saved again: %+v %v", again, err)
	}
	if got := again.spaceStamp(); got != "" {
		t.Errorf("the model-less connector is stamped %q", got)
	}
	// A form that did not render the picker (the service went away) keeps
	// the stored selection rather than falling back to local.
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: "vec", Endpoint: "vec"})
	kept, err := ResolveEmbeddingProvider(EmbeddingConfig{Enabled: true, Endpoint: "vec"})
	if err != nil || kept.Provider != "vec" || kept.Endpoint != "vec" {
		t.Errorf("a blank provider over a stored service: %+v %v", kept, err)
	}
}

// A connection that names no model stamps its vectors "service:<name>",
// as it did when the setting spelt it service:<name> or embed:<name>, so
// naming it plainly leaves every stored vector comparable. The old spelling
// itself names nothing now: it embeds nothing until chosen again.
func TestTheStampOutlastsTheSettingsSpelling(t *testing.T) {
	if got := (EmbeddingConfig{Provider: "vec", Endpoint: "service:vec"}).spaceStamp(); got != "service:vec" {
		t.Errorf("a plainly named model-less connection is stamped %q", got)
	}
	if got := (EmbeddingConfig{Provider: "vec", Model: "e5"}).spaceStamp(); got != "e5" {
		t.Errorf("a named model's stamp = %q", got)
	}
	if _, err := embedTexts(context.Background(), EmbeddingConfig{Enabled: true, Provider: "embed:vec"}, []string{"a"}); err == nil ||
		!strings.Contains(err.Error(), "not set to a connector or a peer") {
		t.Errorf("the old spelling embedded: %v", err)
	}
}

// A connection saved the old way (service:<name>, embed:<name>) and chosen
// again by its name is the same connection: the save keeps the endpoint
// naming its vectors' space, so nothing is re-embedded. Another connection
// is another space.
func TestChoosingTheSameConnectionAgainKeepsTheSpace(t *testing.T) {
	defer SetEmbeddingConfig(EmbeddingConfig{})
	for _, stored := range []EmbeddingConfig{
		{Enabled: true, Provider: "service:vec", Endpoint: "service:vec"},
		{Enabled: true, Provider: "embed:vec", Endpoint: "http://localhost:8080/v1"},
		{Enabled: true, Provider: "vec", Endpoint: "embed:vec"},
	} {
		SetEmbeddingConfig(stored)
		got, err := ResolveEmbeddingProvider(EmbeddingConfig{Enabled: true, Provider: "vec"})
		if err != nil || got.Endpoint != stored.Endpoint || got.Provider != "vec" {
			t.Errorf("over %+v, chosen again: %+v %v", stored, got, err)
		}
		if other, _ := ResolveEmbeddingProvider(EmbeddingConfig{Enabled: true, Provider: "other"}); other.Endpoint != "other" {
			t.Errorf("over %+v, another connection kept the old space: %+v", stored, other)
		}
	}
}
