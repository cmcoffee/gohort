// Speech (TTS): the reverse of transcribe.go. Turns text into audio through an
// OpenAI-compatible /audio/speech endpoint, which the local speech servers
// people run (Kokoro-FastAPI, openedai-speech) and the real OpenAI API all
// serve, so a local deployment just needs the endpoint URL.
//
// Disabled by default: until the admin UI configures an endpoint, every call
// returns an error and callers send their text without a voice.

package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// SpeechTable is the kvlite table holding the persisted speech config. Here
// rather than in core's tables.go, with the rest of speech, so core's
// namespace does not grow for a subsystem that lives in this package.
const SpeechTable = "speech_config"

// SpeechConfig holds the endpoint, model and voice for the OpenAI-compatible
// speech API. Model and Voice name what the server offers: Kokoro-FastAPI
// takes "kokoro" and a voice such as "af_heart", OpenAI "tts-1" and "alloy".
type SpeechConfig struct {
	Endpoint string `json:"endpoint"` // base URL, e.g. http://localhost:8880/v1
	Model    string `json:"model"`    // optional for servers with one model
	Voice    string `json:"voice"`    // optional for servers with a default voice
	APIKey   string `json:"api_key"`  // optional bearer token (real OpenAI / authenticated proxies)
	Enabled  bool   `json:"enabled"`  // false → calls return an error and callers send text only
	// Provider records where the config came from: "local" (typed in) or
	// "peer:<name>" (filled from a peer instance that lends its speech
	// server). Same field, same reason, as TranscribeConfig.Provider.
	Provider string `json:"provider,omitempty"`
}

// SpeechService, when set, speaks through a service connector, for a config
// whose Provider is "service:<name>" (core/service installs it), returning
// the audio and its extension. Nil, and such a config is refused.
var SpeechService func(ctx context.Context, connector, text, voice string) ([]byte, string, error)

// SpeechResolver, when set, rewrites the stored config on every read, so one
// whose Provider names a peer picks up that peer's current endpoint and
// credential. core installs it; this package knows nothing about peers. Same
// seam as TranscribeResolver, and it must be as cheap.
var SpeechResolver func(SpeechConfig) SpeechConfig

// speechClient is what a speech request goes out on: the peer transport for
// a config borrowed from a peer (a credential current at send time), an
// ordinary client otherwise.
func speechClient(cfg SpeechConfig, timeout time.Duration) *http.Client {
	if HTTPClientForProvider != nil {
		if c := HTTPClientForProvider(cfg.Provider, timeout); c != nil {
			return c
		}
	}
	return &http.Client{Timeout: timeout}
}

var (
	speechCfgMu sync.RWMutex
	speechCfg   SpeechConfig
)

// SetSpeechConfig installs the process-wide speech config.
func SetSpeechConfig(cfg SpeechConfig) {
	speechCfgMu.Lock()
	speechCfg = cfg
	speechCfgMu.Unlock()
}

// GetSpeechConfig returns the current speech config, with any registered
// resolver applied.
func GetSpeechConfig() SpeechConfig {
	speechCfgMu.RLock()
	cfg := speechCfg
	speechCfgMu.RUnlock()
	// Outside the lock, as GetTranscribeConfig does: the resolver reads
	// another package's state.
	if SpeechResolver != nil {
		cfg = SpeechResolver(cfg)
	}
	return cfg
}

// LoadSpeechConfigFromDB reads the persisted speech config and installs it.
// Silent no-op when the DB handle is nil or no record exists.
func LoadSpeechConfigFromDB(db ConfigStore) {
	if db == nil {
		return
	}
	var cfg SpeechConfig
	if !db.Get(SpeechTable, "current", &cfg) {
		return
	}
	SetSpeechConfig(cfg)
}

// speechMaxBytes bounds what a speech server may send back. A few minutes of
// mp3 is a few megabytes; anything near this is a server answering with
// something other than the clip that was asked for.
const speechMaxBytes = 32 << 20

// Speak turns text into audio with the configured speech endpoint, returning
// the audio and its file extension (".mp3", ".wav", ".ogg").
//
// Returns an error when speech is disabled or unreachable; callers treat that
// as "send the text without a voice", never as a failed reply.
func Speak(ctx context.Context, text string) ([]byte, string, error) {
	return SpeakWith(ctx, GetSpeechConfig(), text)
}

// SpeakIn is Speak in a given voice, for an agent with a voice of its own.
// An empty voice is the configured one.
func SpeakIn(ctx context.Context, text, voice string) ([]byte, string, error) {
	cfg := GetSpeechConfig()
	if v := strings.TrimSpace(voice); v != "" {
		cfg.Voice = v
	}
	return SpeakWith(ctx, cfg, text)
}

// SpeakWith is Speak against an explicit config, for the admin Test button,
// which tries the form as typed before it is saved.
func SpeakWith(ctx context.Context, cfg SpeechConfig, text string) ([]byte, string, error) {
	if !cfg.Enabled {
		return nil, "", fmt.Errorf("speech disabled")
	}
	if name, ok := strings.CutPrefix(cfg.Provider, ServiceProviderPrefix); ok {
		if SpeechService == nil {
			return nil, "", fmt.Errorf("speech is set to the service %q, and services are not available here", name)
		}
		if text = strings.TrimSpace(text); text == "" {
			return nil, "", fmt.Errorf("speech: no text to speak")
		}
		return SpeechService(ctx, name, text, cfg.Voice)
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, "", fmt.Errorf("speech endpoint not configured")
	}
	if text = strings.TrimSpace(text); text == "" {
		return nil, "", fmt.Errorf("speech: no text to speak")
	}
	req := map[string]string{"input": text, "response_format": "mp3"}
	if cfg.Model != "" {
		req["model"] = cfg.Model
	}
	if cfg.Voice != "" {
		req["voice"] = cfg.Voice
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	url := strings.TrimRight(cfg.Endpoint, "/") + "/audio/speech"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	started := time.Now()
	// 2 minutes is the ceiling for a caller with no deadline of its own; a
	// long reply on a CPU-only server takes a while, and the request is
	// context-aware, so a caller's shorter budget wins.
	resp, err := speechClient(cfg, 2*time.Minute).Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("speech request failed (%s): %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, speechMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("speech: reading the audio: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("speech: HTTP %d: %s", resp.StatusCode, clipSpeechError(body))
	}
	if len(body) > speechMaxBytes {
		return nil, "", fmt.Errorf("speech: the server sent more than %d MB", speechMaxBytes>>20)
	}
	ext, ok := speechExt(body)
	if !ok {
		// A 200 whose body is an error page or a JSON complaint. Played back,
		// it would be a voice note of silence; reported, it says what is wrong.
		return nil, "", fmt.Errorf("speech: the server answered without audio: %s", clipSpeechError(body))
	}
	nfo.Debug("[speech] %d chars -> %d bytes %s in %s", len(text), len(body), ext, time.Since(started).Round(time.Millisecond))
	return body, ext, nil
}

// speechExt names the audio a speech server sent back, and says whether it is
// audio at all. mp3 is what was asked for, and a bare mp3 stream (no ID3 tag)
// sniffs as octet-stream, so that is taken as mp3; text and JSON are not.
func speechExt(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	ct := http.DetectContentType(b)
	switch {
	case ct == "audio/wave":
		return ".wav", true
	case ct == "application/ogg":
		return ".ogg", true
	case ct == "audio/mpeg", ct == "application/octet-stream":
		return ".mp3", true
	case strings.HasPrefix(ct, "audio/"):
		return ".mp3", true
	}
	return "", false
}

// clipSpeechError keeps a server's complaint short enough to be an error.
func clipSpeechError(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// SpeechMIME is the media type a browser plays a clip of this extension as.
func SpeechMIME(ext string) string {
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	}
	return "audio/mpeg"
}

// SpeechVoices lists the voices cfg's server offers, from its GET
// /audio/voices (Kokoro-FastAPI has one; OpenAI does not). Empty for a server
// without the list, or one that cannot be reached: the voice is then typed.
func SpeechVoices(ctx context.Context, cfg SpeechConfig) []string {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cfg.Endpoint, "/")+"/audio/voices", nil)
	if err != nil {
		return nil
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := speechClient(cfg, 0).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Voices []json.RawMessage `json:"voices"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil
	}
	var out []string
	for _, v := range body.Voices {
		if name := speechVoiceName(v); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// speechVoiceName reads one entry of a voice list: a bare name, or an object
// naming it under id or name.
func speechVoiceName(v json.RawMessage) string {
	var name string
	if json.Unmarshal(v, &name) == nil {
		return strings.TrimSpace(name)
	}
	var obj struct{ ID, Name string }
	if json.Unmarshal(v, &obj) == nil {
		if obj.ID != "" {
			return strings.TrimSpace(obj.ID)
		}
		return strings.TrimSpace(obj.Name)
	}
	return ""
}

// VoiceChips is a voice list in the shape a form field's ChipsSource reads.
func VoiceChips(voices []string) []map[string]string {
	out := make([]map[string]string, 0, len(voices))
	for _, v := range voices {
		out = append(out, map[string]string{"id": v, "name": v, "value": v})
	}
	return out
}
