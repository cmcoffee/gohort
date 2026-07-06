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
}

var (
	embedCfgMu sync.RWMutex
	embedCfg   EmbeddingConfig
)

// SetEmbeddingConfig installs the process-wide embedding config. Called
// from config.go during init_database.
func SetEmbeddingConfig(cfg EmbeddingConfig) {
	embedCfgMu.Lock()
	embedCfg = cfg
	embedCfgMu.Unlock()
}

// GetEmbeddingConfig returns the current embedding config.
func GetEmbeddingConfig() EmbeddingConfig {
	embedCfgMu.RLock()
	defer embedCfgMu.RUnlock()
	return embedCfg
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
// Embed embeds text using the globally-configured embedding backend.
func Embed(ctx context.Context, text string) ([]float32, error) {
	return EmbedWith(ctx, GetEmbeddingConfig(), text)
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
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
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
		Embeddings [][]float32    `json:"embeddings"`
		Embedding  []float32      `json:"embedding"`
		Data       []openAIItem   `json:"data"`
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
