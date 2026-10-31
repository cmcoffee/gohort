package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/migrate"
	"github.com/cmcoffee/snugforge/kvlite"
)

// embedServer is an embedder at /v1/embeddings answering in reply's shape,
// keeping each request's key and body.
func embedServer(t *testing.T, reply func(texts []string) any) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var keys []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		keys, bodies = append(keys, r.Header.Get("Authorization")), append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reply(textsOf(body["input"])))
	}))
	t.Cleanup(srv.Close)
	return srv, &keys, &bodies
}

// keepEmbeddingConfig restores the process-wide embedding config after a test.
func keepEmbeddingConfig(t *testing.T) {
	saved := GetEmbeddingConfig()
	t.Cleanup(func() { SetEmbeddingConfig(saved) })
}

// Embeddings this instance made itself become a connector once, at startup:
// the endpoint, model and key it embedded with, in the same space, so the
// stored vectors stay comparable and nothing is re-embedded. The key moves to
// a credential, the settings name the connector, and embedding works through
// it as it did before. Run again, it changes nothing.
func TestEmbeddingSettingsBecomeAConnector(t *testing.T) {
	world(t)
	keepEmbeddingConfig(t)
	srv, keys, bodies := embedServer(t, func(texts []string) any {
		vecs := make([][]float32, len(texts))
		for i := range vecs {
			vecs[i] = []float32{0.1, 0.2, 0.3}
		}
		return map[string]any{"embeddings": vecs} // Ollama's shape
	})
	before := EmbeddingConfig{Enabled: true, Endpoint: srv.URL + "/v1", Model: "nomic-embed-text", APIKey: "sk-local", DocPrefix: "search_document: "}
	RootDB.Set(EmbeddingTable, "current", before)
	SetEmbeddingConfig(before)
	version := EmbedVersion()

	MigrateEmbeddings(RootDB)

	var after EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &after)
	if after.Provider != "service:embeddings" || after.APIKey != "" || after.Endpoint != before.Endpoint || after.Model != before.Model || after.DocPrefix != before.DocPrefix || !after.Enabled {
		t.Fatalf("the settings after: %+v", after)
	}
	if got := EmbedVersion(); got != version {
		t.Errorf("the embedding space moved from %q to %q: every stored vector would be re-embedded", version, got)
	}
	c, ok := GetConnector(RootDB, "embeddings")
	if !ok || !c.Approved || c.Kind != Kind {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	if !strings.Contains(string(c.Spec), `"credential":"embeddings_key"`) {
		t.Errorf("the connector does not use the key's credential: %s", c.Spec)
	}
	vec, err := Embed(context.Background(), "how do I shrink a volume")
	if err != nil || len(vec) != 3 {
		t.Fatalf("embedding through the connector: %v %v", vec, err)
	}
	if len(*keys) != 1 || (*keys)[0] != "Bearer sk-local" || (*bodies)[0]["model"] != "nomic-embed-text" {
		t.Errorf("the request: %v %v", *keys, *bodies)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:embeddings_to_connector:v1:", &m) || !m.Done || m.Error != "" || m.Changed == 0 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	// Once: settings put back the old way are not moved again.
	RootDB.Set(EmbeddingTable, "current", before)
	MigrateEmbeddings(RootDB)
	if _, again := GetConnector(RootDB, "embeddings-2"); again {
		t.Error("the migration ran twice")
	}
}

// A single-model server (llama.cpp, vLLM) is sent no model, as before, and
// whichever shape it answers in is read: OpenAI's list, or one embedding.
// With no model, the stored vectors keep their blank stamp.
func TestAModelLessEmbedderMovesWithItsSpace(t *testing.T) {
	world(t)
	keepEmbeddingConfig(t)
	one := false
	srv, _, bodies := embedServer(t, func(texts []string) any {
		if one {
			return map[string]any{"embedding": []float32{0.5, 0.5}}
		}
		data := make([]map[string]any, len(texts))
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
		}
		return map[string]any{"data": data}
	})
	RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Endpoint: srv.URL + "/v1/"})
	if changed, err := migrateEmbeddings(RootDB); err != nil || changed == 0 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	cfg := GetEmbeddingConfig()
	if cfg.Provider != "service:embeddings" || cfg.Endpoint != srv.URL+"/v1/" {
		t.Fatalf("the settings: %+v", cfg)
	}
	vecs, err := media.EmbedService(context.Background(), "embeddings", "", []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[1][0] != 1 {
		t.Fatalf("a batch, read from data: %v %v", vecs, err)
	}
	if _, sent := (*bodies)[0]["model"]; sent {
		t.Errorf("a single-model server was sent a model: %v", (*bodies)[0])
	}
	one = true
	if vec, err := Embed(context.Background(), "one"); err != nil || len(vec) != 2 || vec[0] != 0.5 {
		t.Errorf("one embedding, read as a list of one: %v %v", vec, err)
	}
	if h := RecordOf("embeddings"); h.Capabilities["embed.text"] == nil {
		t.Error("the connector's row does not count its calls")
	}
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c", EmbeddedChunk{ID: "c", Source: "kb", Text: "t", Vector: []float32{1, 0}})
	if s := VectorStats(db); s.Stale != 0 {
		t.Errorf("a row the old embedder made reads as stale: %+v", s)
	}
}

// A fresh install gets the connector from the defaults (Ollama on this
// machine), with embeddings off until someone turns them on, under a name
// not taken. Settings that already name a connector or a peer are left alone.
func TestAFreshInstallGetsTheDefaultEmbedder(t *testing.T) {
	world(t)
	keepEmbeddingConfig(t)
	install(t, "embeddings", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://api.example.test", "auth": map[string]any{"none": true},
			"operations": map[string]any{"embed": map[string]any{"method": "POST", "path": "/e", "body": map[string]any{"input": "{texts}"}}}},
		"provides": []any{map[string]any{"capability": "embed.text", "steps": []any{map[string]any{"call": "embed"}},
			"output": map[string]any{"vectors": map[string]any{"path": "data", "each": "embedding", "as": "json"}}}},
	})
	if _, err := migrateEmbeddings(RootDB); err != nil {
		t.Fatal(err)
	}
	cfg := GetEmbeddingConfig()
	if cfg.Enabled || cfg.Provider != "service:embeddings-2" || cfg.Endpoint != "http://localhost:11434/api" || cfg.Model != "nomic-embed-text" {
		t.Errorf("a fresh install's settings: %+v", cfg)
	}
	if got := serviceAddress("embeddings-2"); got != "http://localhost:11434/api" {
		t.Errorf("the default connector is at %q", got)
	}
	for _, p := range []string{"peer:den", "service:other"} {
		RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: p})
		if changed, err := migrateEmbeddings(RootDB); err != nil || changed != 0 {
			t.Errorf("settings naming %s were moved: %d %v", p, changed, err)
		}
	}
}

// A connector whose host answered "too many requests" is left alone until
// the time it named: a call that cannot wait that long fails at once without
// sending, rather than adding to the refusals.
func TestARateLimitedConnectorIsWaitedOut(t *testing.T) {
	world(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"rate limit reached"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	install(t, "busy", embedMigrationSpec(EmbeddingConfig{Endpoint: srv.URL, Model: "m"}, ""))
	if _, err := Call(context.Background(), nil, "busy", "embed.text", map[string]any{"texts": []string{"a"}}); err == nil {
		t.Fatal("the refused call reported success")
	}
	if d := media.PausedFor(srv.URL); d < 25*time.Second || d > 30*time.Second {
		t.Errorf("paused for %s, want the 30s Retry-After", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Call(ctx, nil, "busy", "embed.text", map[string]any{"texts": []string{"b"}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || atomic.LoadInt32(&calls) != 1 {
		t.Errorf("err %v after %d call(s)", err, calls)
	}
}

// An output's other places are outputs of their own, one level deep, and
// single is for a json output read as one item.
func TestOutputAlternativesAreChecked(t *testing.T) {
	spec := func(vectors map[string]any) map[string]any {
		s := embedMigrationSpec(EmbeddingConfig{Endpoint: "http://localhost:8080/v1"}, "")
		s["provides"].([]any)[0].(map[string]any)["output"] = map[string]any{"vectors": vectors}
		return s
	}
	for _, c := range []struct {
		vectors map[string]any
		want    string
	}{
		{map[string]any{"path": "data", "or": []any{map[string]any{"path": "a", "or": []any{map[string]any{"path": "b"}}}}}, "has an or of its own"},
		{map[string]any{"path": "data", "or": []any{map[string]any{"path": "x", "as": "url"}}}, "or 1"},
		{map[string]any{"path": "data", "each": "embedding", "single": true}, "single reads one item"},
	} {
		raw, _ := json.Marshal(spec(c.vectors))
		s, err := Parse(raw)
		if err == nil {
			err = s.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.vectors, err)
		}
	}
}
