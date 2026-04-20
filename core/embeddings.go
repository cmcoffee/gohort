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
	Endpoint string // base URL, e.g. http://localhost:11434
	Model    string // e.g. nomic-embed-text
	Enabled  bool   // false → ingestion and search become no-ops
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
// currently configured endpoint/model. Calls the ollama-style
// `/api/embed` endpoint which both Ollama and compatible servers
// expose. Returns an error when embeddings are disabled or the
// endpoint is unreachable — caller should treat this as a skip
// condition, not a fatal error.
func Embed(ctx context.Context, text string) ([]float32, error) {
	cfg := GetEmbeddingConfig()
	if !cfg.Enabled {
		return nil, fmt.Errorf("embeddings disabled")
	}
	if cfg.Endpoint == "" || cfg.Model == "" {
		return nil, fmt.Errorf("embedding endpoint or model not configured")
	}
	payload, _ := json.Marshal(struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{cfg.Model, text})

	url := strings.TrimRight(cfg.Endpoint, "/") + "/api/embed"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

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
	// Older versions returned {"embedding": [...]}. Handle both.
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
		Embedding  []float32   `json:"embedding"`
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
