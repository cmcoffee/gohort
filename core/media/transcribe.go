// Audio transcription (STT): Transcribe(ctx, audioBytes) is how the rest of
// the framework hears speech, as Embed(ctx, text) is how it reads meaning.
// The transcriber is a service connector (core/service, speech.transcribe)
// or a peer that lends transcription: this instance has none of its own, so
// what transcribes can be read, tested, fixed and replaced like any other
// connector. A peer is reached here, over the OpenAI-shaped
// /audio/transcriptions its peer API serves.
//
// Disabled by default: until it is turned on and pointed at a connector or a
// peer, every call returns an error and callers treat it as a skip
// condition (videodl falls back to frames-only). Failures are non-fatal at
// every layer.

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

// TranscribeConfig is the transcription settings: on or off, and what
// transcribes. For a peer, Endpoint, Model and APIKey are filled from the
// peer's record (ResolveTranscribeProvider), and kept current on every read;
// for a service connector, Model is the one it declares (what a peer
// borrowing transcription from here is told), and the rest is the
// connector's own.
type TranscribeConfig struct {
	Endpoint string `json:"endpoint"` // a peer's /api/peer/v1
	Model    string `json:"model"`    // the model it transcribes with, when known
	APIKey   string `json:"api_key"`  // a peer's credential
	Enabled  bool   `json:"enabled"`  // false → calls return an error and callers skip
	// Provider is what transcribes: a connection's name or
	// "peer:<name>". "local", or blank, is the endpoint this instance once
	// called itself: it no longer transcribes.
	Provider string `json:"provider,omitempty"`
}

// peerProviderPrefix marks a config whose Provider names a peer. core keeps
// the peers; this package only needs to tell one from a connector.
const peerProviderPrefix = "peer:"

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

// TranscribeResolver, when set, gets the chance to rewrite the stored config
// on every read. The core package installs it at startup so a config whose
// Provider names a peer picks up that peer's CURRENT endpoint and key instead
// of the copy taken when the operator saved the form.
//
// A function VARIABLE for the same reason GovernedUploadFunc below is one: core
// imports this package, so this package cannot import core and knows nothing
// about peers. Nil means the stored config is used verbatim, which is exactly
// the old behavior.
//
// It must be cheap — GetTranscribeConfig sits on the enabled-check path that
// runs per upload and per page render. The core side caches its peer lookup for
// a second, which is what makes that true.
var TranscribeResolver func(TranscribeConfig) TranscribeConfig

// HTTPClientForProvider, when set, supplies the client a transcription request
// goes out on. core installs it so a config whose Provider names a PEER gets
// the peer transport: a credential resolved at send time rather than the copy
// this config is carrying, and a re-exchange when the peer refuses it.
//
// A function variable for the same reason as the two seams around it — this
// package cannot import core and knows nothing about peers. Nil, or a Provider
// that names no peer, means an ordinary client with the given timeout, which is
// exactly the old behavior.
//
// The alternative was for this file to keep sending cfg.APIKey, which is the
// credential that was live when the config was READ. Reads happen far from
// sends, and a peer credential rotates between them.
var HTTPClientForProvider func(provider string, timeout time.Duration) *http.Client

// A setting names the connection that does its job by the connection's own
// name (serper-search). Beside it are the names a page offers as its own
// (Reserved: duckduckgo, smtp, none, ...) and "peer:<name>" for a peer. A
// connection cannot take a reserved name, so a value is never both.
//
// Settings were once written "<job>:<name>" (search:serper-search,
// embed:local-embeddings, and service:<name> before that). That shape is
// no longer written or read as naming anything: LegacyConnection recognises
// it, so the page can say it needs setting again, and so what was stamped
// with it (the embeddings' space) is kept when the same connection is
// chosen again.

// legacyJobs are the prefixes a setting's value once named a connection by.
var legacyJobs = map[string]bool{"llm": true, "embed": true, "transcribe": true, "speak": true,
	"search": true, "render": true, "mail": true, "convert": true, "service": true}

// reserved are the names pages offer as their own: what this instance does
// itself, the built-in LLM providers, and the words that mean nothing or
// the default. No connection may take one.
var reserved = map[string]bool{
	"none": true, "default": true, "local": true, "builtin": true, "commands": true,
	"duckduckgo": true, "searxng": true, "smtp": true,
	"anthropic": true, "openai": true, "gemini": true, "bedrock": true,
	"ollama": true, "llama.cpp": true, "vllm": true,
}

// Reserved reports whether name is one a page offers as its own, which no
// connection may take.
func Reserved(name string) bool { return reserved[strings.ToLower(strings.TrimSpace(name))] }

// ChosenConnection is the connection a setting's value names: the value
// itself, unless it is a reserved name, a peer's, or in the old
// "<job>:<name>" shape; false for those and for nothing.
func ChosenConnection(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" || strings.Contains(v, ":") || Reserved(v) {
		return "", false
	}
	return v, true
}

// IsConnection reports whether a setting's value names a connection.
func IsConnection(value string) bool {
	_, ok := ChosenConnection(value)
	return ok
}

// LegacyConnection is the connection a value in the old "<job>:<name>"
// shape named. Such a value names nothing now; this says which connection it
// meant, so it can be set again.
func LegacyConnection(value string) (string, bool) {
	job, name, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || !legacyJobs[job] || strings.TrimSpace(name) == "" {
		return "", false
	}
	return strings.TrimSpace(name), true
}

// EmbedService, when set, embeds through a service connector, for an
// embedding config whose Provider names a connection (core/service installs
// it): one vector per text, in order, made with model (blank: the service's
// own). Nil, and such a config is refused. It sits with the other service
// seams here although embedding lives in core, which keeps its exports few.
var EmbedService func(ctx context.Context, connector, model string, texts []string) ([][]float32, error)

// ServiceAddress, when set, is the address the approved service connector
// named connector reaches (its base URL), or "" when there is none (core/service
// installs it). Embedding reads it to queue a connector on this machine or its
// network behind the local model, and to wait out the pause its host asked for.
var ServiceAddress func(connector string) string

// PageService, when set, renders a page through a service connector, for a
// page rendering source naming a connection (core/service installs it): the
// page's readable text, at most maxChars of it. It sits with the other
// service seams although page rendering is routed in core, for the same
// reason as EmbedService.
var PageService func(ctx context.Context, connector, url string, maxChars int) (string, error)

// MailService, when set, sends mail through a service connector, for a mail
// provider naming a connection (core/service installs it). from may be blank:
// the connector's own sender then. Kept here for the same reason as
// EmbedService.
var MailService func(ctx context.Context, connector, from, to, subject, body string) error

// TranscribeService, when set, transcribes through a service connector, for a
// config whose Provider names one. core/service installs it; this package
// knows nothing about connectors. Nil, and such a config is refused.
var TranscribeService func(ctx context.Context, connector string, audio []byte, filename string) (string, error)

// transcribeClient returns what this request should be sent on.
func transcribeClient(cfg TranscribeConfig, timeout time.Duration) *http.Client {
	if HTTPClientForProvider != nil {
		if c := HTTPClientForProvider(cfg.Provider, timeout); c != nil {
			return c
		}
	}
	return &http.Client{Timeout: timeout}
}

// GetTranscribeConfig returns the current STT config, with any registered
// resolver applied.
func GetTranscribeConfig() TranscribeConfig {
	transcribeCfgMu.RLock()
	cfg := transcribeCfg
	transcribeCfgMu.RUnlock()
	// Resolved OUTSIDE the lock: the resolver reaches into another package's
	// state to read a peer record, and holding this mutex across that is how
	// two independent caches become one deadlock.
	if TranscribeResolver != nil {
		cfg = TranscribeResolver(cfg)
	}
	return cfg
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

// GovernedUploadFunc, when set, is how Transcribe reaches the network. The
// core package installs it at startup so the audio goes out through the
// credentialed dispatch — URL allow-list, audit entry, rate limit, and the
// Private-mode kill switch all apply.
//
// It is a function VARIABLE rather than a direct call because core imports this
// package, so this package cannot import core. Same seam as GeminiKeyFunc and
// friends.
//
// Nil means the raw fallback below runs: an http.Client of our own, with none
// of those controls. That was the ONLY path until this seam existed — user
// audio left the machine on an unaudited connection — so a deployment that
// wires this is strictly better off, and one that doesn't is no worse than it
// was.
// bearer carries the endpoint's API key. It is a parameter rather than
// something the core side reads for itself because this seam is generic — any
// media upload rides it — and only the caller knows which config the URL came
// from. Omitting it was silent: the governed path synthesized an
// unauthenticated credential, so a local whisper.cpp kept working while every
// AUTHENTICATED endpoint (real OpenAI, a peer instance) lost its key on the way
// out and answered 401 to a configuration that looked correct.
var GovernedUploadFunc func(ctx context.Context, up UploadRequest) (string, error)

// UploadRequest is one governed upload. A struct rather than eight positional
// arguments, and Provider is why it became one: the core side cannot resolve a
// PEER's credential for itself without knowing that the URL came from a peer
// config, and Bearer alone cannot tell it — a bearer is just a string, and the
// one in a stored config is the credential that was live when the config was
// READ.
type UploadRequest struct {
	URL       string
	FieldName string
	FileName  string
	Body      []byte
	Fields    map[string]string
	// Bearer is the endpoint's key as the config holds it. For a peer it is a
	// FALLBACK: the core side resolves a current credential from Provider and
	// uses this only when there is no peer to resolve against.
	Bearer string
	// Provider records where the config came from — "local", or "peer:<name>".
	// The core side reads it to decide whether this upload rides the peer
	// transport (credential resolved at send time, re-exchanged if refused) or
	// goes out as written.
	Provider string
}

// Transcribe sends audio bytes to the configured connector or peer and
// returns the recognized text. Filename hints the server about the
// container/codec (whisper.cpp inspects the extension); pass a
// representative name like "audio.mp3" or "clip.wav".
//
// Returns ("", error) when STT is disabled or unreachable — callers
// should treat this as a skip condition, not a fatal error.
//
// By the transcription job (TranscribeHook, core/flows) when it is loaded:
// the connector or peer configured here is its native flow, and an admin can
// adapt it to a command (a local whisper.cpp, say) where none is set up.
func Transcribe(ctx context.Context, audio []byte, filename string) (string, error) {
	if TranscribeHook != nil {
		return TranscribeHook(ctx, audio, filename)
	}
	return TranscribeWith(ctx, GetTranscribeConfig(), audio, filename)
}

// WithTranscribePrompt carries words the speaker is likely to say, names
// most of all, to the transcription a ctx reaches: they go as the request's
// prompt, which an OpenAI-shaped transcriber takes as text that came before
// the clip, and so spells a name it would otherwise hear as some other word.
// On the context rather than a parameter, so it reaches the transcriber
// through the transcription job and a peer without a signature changing on
// the way. Bounded to transcribePromptMax.
func WithTranscribePrompt(ctx context.Context, prompt string) context.Context {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ctx
	}
	if r := []rune(prompt); len(r) > transcribePromptMax {
		prompt = string(r[:transcribePromptMax])
	}
	return context.WithValue(ctx, transcribePromptKey{}, prompt)
}

// TranscribePrompt is the prompt WithTranscribePrompt put on ctx, "" for none.
func TranscribePrompt(ctx context.Context) string {
	s, _ := ctx.Value(transcribePromptKey{}).(string)
	return s
}

type transcribePromptKey struct{}

// transcribePromptMax bounds a prompt: Whisper reads at most 224 tokens of
// one, and a list of names needs far fewer.
const transcribePromptMax = 400

// TranscribeWith is Transcribe against an EXPLICIT config rather than the
// process-wide one.
//
// Mirrors EmbedWith, and exists for the same reason: the process-global config
// makes it impossible for one instance to speak to a peer while also SERVING
// one, which is exactly the arrangement resource sharing creates. A caller that
// has already resolved a config — the peer path, a connectivity test — passes
// it here instead of installing it globally first.
func TranscribeWith(ctx context.Context, cfg TranscribeConfig, audio []byte, filename string) (string, error) {
	if !cfg.Enabled {
		nfo.Debug("[transcribe] disabled (cfg.Enabled=false)")
		return "", fmt.Errorf("transcription disabled")
	}
	if name, ok := ChosenConnection(cfg.Provider); ok {
		if TranscribeService == nil {
			return "", fmt.Errorf("transcription is set to the service %q, and services are not available here", name)
		}
		if len(audio) == 0 {
			return "", fmt.Errorf("transcription: empty audio")
		}
		return TranscribeService(ctx, name, audio, filename)
	}
	if !strings.HasPrefix(cfg.Provider, peerProviderPrefix) {
		return "", fmt.Errorf("transcription is not set to a connector or a peer: choose one under Admin > Services > Audio Transcription (STT)")
	}
	return transcribeFromPeer(ctx, cfg, audio, filename)
}

// transcribeFromPeer sends audio to a peer's /audio/transcriptions, the
// OpenAI shape its peer API serves, with the peer's current credential.
func transcribeFromPeer(ctx context.Context, cfg TranscribeConfig, audio []byte, filename string) (string, error) {
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

	url := strings.TrimRight(cfg.Endpoint, "/") + "/audio/transcriptions"
	if GovernedUploadFunc != nil {
		fields := map[string]string{"response_format": "text"}
		if cfg.Model != "" {
			fields["model"] = cfg.Model
		}
		if prompt := TranscribePrompt(ctx); prompt != "" {
			fields["prompt"] = prompt
		}
		out, err := GovernedUploadFunc(ctx, UploadRequest{
			URL: url, FieldName: "file", FileName: filepath.Base(filename),
			Body: audio, Fields: fields, Bearer: cfg.APIKey, Provider: cfg.Provider,
		})
		if err != nil {
			nfo.Debug("[transcribe] governed upload failed: %v", err)
			return "", err
		}
		return strings.TrimSpace(out), nil
	}

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
	if prompt := TranscribePrompt(ctx); prompt != "" {
		_ = mw.WriteField("prompt", prompt)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("transcribe: close: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	// 5 minutes: transcription of a long recording is slow, and a deadline
	// shorter than the work turns a working answer into a failure.
	resp, err := transcribeClient(cfg, 5*time.Minute).Do(req)
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
