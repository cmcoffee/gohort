package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EmbeddingConfig holds the endpoint + model for the ollama-compatible
// embeddings API used by the semantic-search pipeline. Defaults pull
// from the worker LLM's endpoint and nomic-embed-text so a typical
// ollama deployment needs no extra configuration beyond `ollama pull
// nomic-embed-text`. Set via --setup or the admin WebUI.
type EmbeddingConfig struct {
	Endpoint string `json:"endpoint"` // base URL, e.g. http://localhost:11434/api
	Model    string `json:"model"`    // optional — required for Ollama, ignored by single-model backends (llama.cpp, vLLM, hf-tei)
	APIKey   string `json:"api_key"`  // optional bearer token (OpenAI hosted, authenticated proxies)
	Enabled  bool   `json:"enabled"`  // false → ingestion and search become no-ops
	// Provider records WHERE this config came from: "local" (the fields above
	// were typed in) or "peer:<name>" (they were resolved from a registered
	// peer instance — see ResolveEmbeddingProvider).
	//
	// It is NOT bookkeeping only: GetEmbeddingConfig reads it and overlays the
	// named peer's current endpoint, model and key, so the fields above are a
	// last-known cache rather than the operative values. That indirection is
	// what makes rotating a peer key take effect without editing this record.
	// Blank means local, which is what every config stored before peers existed
	// says.
	Provider string `json:"provider,omitempty"`
}

var (
	embedCfgMu sync.RWMutex
	embedCfg   EmbeddingConfig
)

// EmbedVersion identifies the embedding SPACE cached vectors live in —
// model + endpoint. Vectors cached under a different version must never be
// cosine-compared against fresh ones: a same-dimension model swap otherwise
// silently degrades every cached-vector comparison (fact dedup, the
// supersession band, chunk search) with no visible failure. Consumers treat
// a version mismatch as "no cached vector" and re-embed on touch.
// Limitation: single-model backends (llama.cpp) ignore Model, so a model
// swapped behind the SAME endpoint is undetectable — change the endpoint
// (or set Model informationally) when swapping embedders there.
func EmbedVersion() string {
	cfg := GetEmbeddingConfig()
	return strings.TrimSpace(cfg.Model) + "@" + strings.TrimSpace(cfg.Endpoint)
}

// SetEmbeddingConfig installs the process-wide embedding config. Called
// from config.go during init_database.
func SetEmbeddingConfig(cfg EmbeddingConfig) {
	embedCfgMu.Lock()
	embedCfg = cfg
	embedCfgMu.Unlock()
}

// GetEmbeddingConfig returns the current embedding config.
//
// When it names a peer, the endpoint, model and key are read from that peer's
// CURRENT record rather than from whatever was copied in when the form was
// saved. Every consumer — Embed, EmbedVersion, the peer manifest, the admin
// test button — goes through here, so this is the one place that has to know.
func GetEmbeddingConfig() EmbeddingConfig {
	embedCfgMu.RLock()
	cfg := embedCfg
	embedCfgMu.RUnlock()
	return resolveEmbeddingPeer(cfg)
}

// LoadEmbeddingConfigFromDB reads persisted embedding config from the
// kvlite DB and installs it via SetEmbeddingConfig. Silent no-op when
// the DB handle is nil or no record exists.
func LoadEmbeddingConfigFromDB(db Database) {
	if db == nil {
		return
	}
	var cfg EmbeddingConfig
	if !db.Get(EmbeddingTable, "current", &cfg) {
		return
	}
	SetEmbeddingConfig(cfg)
}

// SaveEmbeddingConfigToDB persists the given config and updates the
// process-wide config in memory.
func SaveEmbeddingConfigToDB(db Database, cfg EmbeddingConfig) error {
	if db == nil {
		return fmt.Errorf("no database available")
	}
	db.Set(EmbeddingTable, "current", cfg)
	SetEmbeddingConfig(cfg)
	return nil
}

// Embed returns the embedding vector for the given text under the
// currently configured endpoint/model. Appends `/embeddings` to the
// configured endpoint, so the configured base URL must already include
// the API-version prefix (Ollama: http://host:port/api, llama.cpp /
// vLLM / OpenAI: http://host:port/v1). Response parser accepts all
// three response shapes — Ollama native ({embeddings:[[...]]}), older
// Ollama ({embedding:[...]}), and OpenAI ({data:[{embedding:[...]}]}).
// Returns an error when embeddings are disabled or the endpoint is
// unreachable — caller should treat this as a skip condition, not a
// fatal error.
// embedSlowWarn is when an embed stops being a detail and becomes the reason a
// message felt slow. Well under the recall-hint budget, so a hint that is about
// to be abandoned still leaves a trace explaining why.
const embedSlowWarn = 1500 * time.Millisecond

// embedOutcome renders what happened for the log line — a transport failure and
// a 500 are different problems and the operator should not have to guess which.
func embedOutcome(err error, resp *http.Response) string {
	if err != nil {
		return "FAILED"
	}
	if resp != nil && resp.StatusCode != 200 {
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return "ok"
}

// Embed embeds text using the globally-configured embedding backend.
func Embed(ctx context.Context, text string) ([]float32, error) {
	return EmbedWith(ctx, GetEmbeddingConfig(), text)
}

// embedCallerKey carries who an embed is on behalf of, for fair queueing.
type embedCallerKey struct{}

// embedCallerLocal is the default: work this instance started for itself.
const embedCallerLocal = "local"

// WithEmbedCaller labels embeds made under ctx, so the scheduler can round-robin
// between them and everyone else. Without a label every embed looks like the
// same caller and fair-share has nothing to be fair between — a peer running
// bulk ingestion would be indistinguishable from the local turn it is delaying.
func WithEmbedCaller(ctx context.Context, caller string) context.Context {
	if caller = strings.TrimSpace(caller); caller == "" {
		return ctx
	}
	return context.WithValue(ctx, embedCallerKey{}, caller)
}

// embedCaller reads the label, defaulting to local.
func embedCaller(ctx context.Context) string {
	if ctx == nil {
		return embedCallerLocal
	}
	if s, ok := ctx.Value(embedCallerKey{}).(string); ok && s != "" {
		return s
	}
	return embedCallerLocal
}

// EmbedWith embeds text using an explicitly-provided embedding config, so a
// caller (an SDK consumer, an injected AppCore) can supply its own backend
// without touching the process-global config. SDK Phase 1.
func EmbedWith(ctx context.Context, cfg EmbeddingConfig, text string) ([]float32, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("embeddings disabled")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("embedding endpoint not configured")
	}
	// Model is optional — single-model backends (llama.cpp,
	// vLLM, hf-tei) load one model at startup and ignore the field.
	// Omit it from the payload when blank so those servers don't 4xx
	// on an unknown model name; Ollama still requires it.
	var payload []byte
	if cfg.Model != "" {
		payload, _ = json.Marshal(struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}{cfg.Model, []string{text}})
	} else {
		payload, _ = json.Marshal(struct {
			Input []string `json:"input"`
		}{[]string{text}})
	}

	url := strings.TrimRight(cfg.Endpoint, "/") + "/embeddings"

	// Queue behind the local model scheduler, the same one chat completions
	// use. An embed is usually served by the SAME process as the worker model,
	// so an unscheduled one competes with chat for exactly the resource the
	// queue exists to protect.
	//
	// Not when the embedder is a PEER: that work happens on another instance,
	// which schedules it against its own load. Queueing it here would stall it
	// behind a local model that is not doing it.
	if !strings.Contains(cfg.Endpoint, "/api/peer/") {
		release, err := AcquireEmbedSlot(ctx, embedCaller(ctx))
		if err != nil {
			return nil, fmt.Errorf("embed queue: %w", err)
		}
		defer release()
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	// 60s is the CEILING for a caller that sets no deadline (bulk ingestion).
	// Latency-sensitive callers pass a ctx with their own budget — the request
	// is context-aware, so the shorter of the two wins.
	//
	// Through PeerClientForProvider, so a peer-backed embedder authenticates
	// itself: the Authorization set above carries whatever key was live when
	// this config was READ, and reads happen far from sends. When the provider
	// names a peer the transport replaces it with a credential resolved now and
	// re-exchanges if the peer refuses it; otherwise this is a plain client.
	client := PeerClientForProvider(cfg.Provider, 60*time.Second)
	started := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(started)
	// A blocking call on the turn's critical path has to be visible. This one
	// logged NOTHING, so 45 seconds of a 50-second turn appeared in the log as
	// a silent gap between two unrelated lines, and finding it took reading the
	// source to work out what could even live there. Slow embeds warn without
	// DEBUG on, because that is the case an operator needs to see.
	if elapsed > embedSlowWarn {
		Log("[embed] SLOW %s in %s (endpoint=%s, %d chars) — an embed on a turn's critical path delays the whole message; check whether this endpoint shares a server with the worker model",
			embedOutcome(err, resp), elapsed.Round(time.Millisecond), url, len(text))
	} else {
		Debug("[embed] %s in %s (endpoint=%s, %d chars)", embedOutcome(err, resp), elapsed.Round(time.Millisecond), url, len(text))
	}
	if err != nil {
		return nil, fmt.Errorf("embed request failed (%s): %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// Include the URL and response body in the error so the
		// operator can see at a glance whether the request even
		// reached the intended endpoint, or accidentally landed on
		// a different HTTP server (e.g., gohort itself, a proxy).
		bodySnip := strings.TrimSpace(string(body))
		if len(bodySnip) > 160 {
			bodySnip = bodySnip[:160] + "..."
		}
		return nil, fmt.Errorf("embed HTTP %d from %s: %s", resp.StatusCode, url, bodySnip)
	}

	// Ollama returns {"embeddings": [[float, float, ...]], "model": "..."}.
	// Older versions returned {"embedding": [...]}. OpenAI-compatible
	// servers (llama.cpp, etc.) return {"data": [{"embedding": [...]}]}.
	// Handle all three.
	type openAIItem struct {
		Embedding []float32 `json:"embedding"`
	}
	var out struct {
		Embeddings [][]float32  `json:"embeddings"`
		Embedding  []float32    `json:"embedding"`
		Data       []openAIItem `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("embed response parse: %w", err)
	}
	if len(out.Embeddings) > 0 && len(out.Embeddings[0]) > 0 {
		return out.Embeddings[0], nil
	}
	if len(out.Embedding) > 0 {
		return out.Embedding, nil
	}
	if len(out.Data) > 0 && len(out.Data[0].Embedding) > 0 {
		return out.Data[0].Embedding, nil
	}
	return nil, fmt.Errorf("embed response had no vector")
}

// Cosine returns the cosine similarity between two vectors. Returns 0
// for mismatched dimensions or zero vectors — safe default for ranking
// without blowing up the search loop.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i, v := range a {
		va := float64(v)
		vb := float64(b[i])
		dot += va * vb
		na += va * va
		nb += vb * vb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
