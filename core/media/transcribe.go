// Audio transcription (STT) — mirrors the embeddings.go shape so the
// rest of the framework can call Transcribe(ctx, audioBytes) the same
// way it calls Embed(ctx, text). Targets the OpenAI-compatible
// /audio/transcriptions endpoint (whisper.cpp server emulates this;
// so does the real OpenAI API), so a typical local-Whisper deployment
// just needs the endpoint URL.
//
// Disabled by default — until --setup or the admin UI configures an
// endpoint, every call returns an error and callers treat it as a
// skip condition (videodl falls back to frames-only). Failures are
// non-fatal at every layer.

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// transcribeTable is the kvlite table holding the persisted STT config. Mirrors
// core's tables.go TranscribeTable — a leaf can't import core, and the value is
// just a table key, so a local copy keeps media dependency-free.
const transcribeTable = "transcribe_config"

// ConfigStore is the minimal key-value store the transcribe config persistence
// needs. core's Database interface satisfies it structurally, so callers pass
// their Database directly while media stays free of a core (or kvlite) import.
type ConfigStore interface {
	Get(table, key string, output any) bool
	Set(table, key string, value any)
}

// TranscribeConfig holds the endpoint + model for the OpenAI-compatible
// audio-transcription API. Model is optional — many local Whisper
// servers ignore it; OpenAI uses "whisper-1".
type TranscribeConfig struct {
	Endpoint string `json:"endpoint"` // base URL, e.g. http://localhost:8080/v1
	Model    string `json:"model"`    // optional, e.g. "whisper-1"
	APIKey   string `json:"api_key"`  // optional bearer token (real OpenAI / authenticated proxies)
	Enabled  bool   `json:"enabled"`  // false → calls return an error and callers skip
}

var (
	transcribeCfgMu sync.RWMutex
	transcribeCfg   TranscribeConfig
)

// SetTranscribeConfig installs the process-wide STT config. Called
// from config.go during init_database.
func SetTranscribeConfig(cfg TranscribeConfig) {
	transcribeCfgMu.Lock()
	transcribeCfg = cfg
	transcribeCfgMu.Unlock()
}

// GetTranscribeConfig returns the current STT config.
func GetTranscribeConfig() TranscribeConfig {
	transcribeCfgMu.RLock()
	defer transcribeCfgMu.RUnlock()
	return transcribeCfg
}

// LoadTranscribeConfigFromDB reads persisted STT config from the kvlite
// DB and installs it. Silent no-op when the DB handle is nil or no
// record exists.
func LoadTranscribeConfigFromDB(db ConfigStore) {
	if db == nil {
		return
	}
	var cfg TranscribeConfig
	if !db.Get(transcribeTable, "current", &cfg) {
		return
	}
	SetTranscribeConfig(cfg)
}

// SaveTranscribeConfigToDB persists the given config and updates the
// process-wide config in memory.
func SaveTranscribeConfigToDB(db ConfigStore, cfg TranscribeConfig) error {
	if db == nil {
		return fmt.Errorf("no database available")
	}
	db.Set(transcribeTable, "current", cfg)
	SetTranscribeConfig(cfg)
	return nil
}

// TranscribeRuntimeFlagScript returns a `<script>` snippet that sets
// the client-side runtime flag `window.GOHORT_TRANSCRIBE_ENABLED`.
// Apps embed this in Page.ExtraHeadHTML so the paperclip's file-picker
// JS can build its accept attribute conditionally — audio file types
// only appear in the picker when whisper is actually configured.
// Empty string when transcription is disabled so the caller can
// concat without branching.
func TranscribeRuntimeFlagScript() string {
	if GetTranscribeConfig().Enabled {
		return `<script>window.GOHORT_TRANSCRIBE_ENABLED = true;</script>`
	}
	return `<script>window.GOHORT_TRANSCRIBE_ENABLED = false;</script>`
}

// Transcribe sends audio bytes to the configured STT endpoint and
// returns the recognized text. Filename hints the server about the
// container/codec (whisper.cpp inspects the extension); pass a
// representative name like "audio.mp3" or "clip.wav".
//
// Returns ("", error) when STT is disabled or unreachable — callers
// should treat this as a skip condition, not a fatal error.
func Transcribe(ctx context.Context, audio []byte, filename string) (string, error) {
	cfg := GetTranscribeConfig()
	if !cfg.Enabled {
		nfo.Debug("[transcribe] disabled (cfg.Enabled=false)")
		return "", fmt.Errorf("transcription disabled")
	}
	if cfg.Endpoint == "" {
		nfo.Debug("[transcribe] no endpoint configured")
		return "", fmt.Errorf("transcription endpoint not configured")
	}
	if len(audio) == 0 {
		nfo.Debug("[transcribe] empty audio buffer")
		return "", fmt.Errorf("transcription: empty audio")
	}
	if filename == "" {
		filename = "audio.mp3"
	}
	nfo.Debug("[transcribe] POST %s/audio/transcriptions filename=%q bytes=%d model=%q",
		strings.TrimRight(cfg.Endpoint, "/"), filename, len(audio), cfg.Model)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	// "file" is the OpenAI-compatible field name.
	fw, err := mw.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", fmt.Errorf("transcribe: form: %w", err)
	}
	if _, err := fw.Write(audio); err != nil {
		return "", fmt.Errorf("transcribe: write: %w", err)
	}
	if cfg.Model != "" {
		_ = mw.WriteField("model", cfg.Model)
	}
	// Ask for the plain text response shape — easier to parse than
	// the verbose JSON that includes timestamps. The OpenAI API +
	// whisper.cpp both accept "text" here and return raw text.
	_ = mw.WriteField("response_format", "text")
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("transcribe: close: %w", err)
	}

	url := strings.TrimRight(cfg.Endpoint, "/") + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		nfo.Debug("[transcribe] transport error: %v", err)
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	nfo.Debug("[transcribe] HTTP %d (%d-byte response)", resp.StatusCode, len(respBody))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("transcribe: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// response_format=text means the body is plain text. Some servers
	// still return JSON despite the request — try JSON first, fall
	// back to raw.
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Text != "" {
		return strings.TrimSpace(parsed.Text), nil
	}
	return strings.TrimSpace(string(respBody)), nil
}
