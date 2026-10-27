package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
)

// registerMediaRoutes wires the media API under the admin sub-mux.
func (a *AdminApp) registerMediaRoutes(sub *http.ServeMux) {
	// Embeddings config — GET returns current settings, POST persists +
	// reinstalls the live config so the next ingestion/search call picks
	// up the new endpoint/model without a restart.
	sub.HandleFunc("/api/embeddings", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleEmbeddings, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleEmbeddingsSave, a.gate()),
	}.Serve)

	// Embeddings connectivity test — POSTs current form values (NOT the
	// saved DB record), runs a one-shot Embed() against them, returns
	// {ok, message|error} for inline display in the admin FormPanel.
	sub.HandleFunc("/api/embeddings/test", jsonapi.Post(a.db, a.handleEmbeddingsTest, a.gate()))

	// /api/embeddings/models — probe the saved embedding endpoint for
	// available models. Returns chip-shaped JSON (id/name/value) so
	// FormField.ChipsSource can render a click-to-fill row above the
	// model input. Tries OpenAI-style /models first (works for OpenAI,
	// vLLM, llama.cpp, hf-tei), falls back to Ollama's /api/tags by
	// transforming the endpoint base. Empty array on either reach
	// failure — the field stays manually editable, no UI error.
	sub.HandleFunc("/api/embeddings/models", jsonapi.Get(a.db, a.handleEmbeddingModels, a.gate()))

	// Audio transcription (STT) — GET/POST. POST persists + reinstalls
	// the live TranscribeConfig so the next Transcribe() call picks up
	// the new endpoint/model/key without a restart.
	sub.HandleFunc("/api/transcribe", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleTranscribe, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleTranscribeSave, a.gate()),
	}.Serve)

	// Image generation — provider + api_key live in per-key rows under
	// ImageTable. Shape mirrors the legacy --setup wiring so existing
	// installs read/write the same kvlite keys.
	sub.HandleFunc("/api/image-gen", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleImageGen, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleImageGenSave, a.gate()),
	}.Serve)

	// STT connectivity test — GET {endpoint}/models with auth header so
	// the operator can confirm reachability + credentials without needing
	// a sample audio file. Both whisper.cpp and the real OpenAI API
	// expose /models on the OpenAI-compatible base; a 2xx means the
	// endpoint is reachable and (if a key was provided) accepts it.
	sub.HandleFunc("/api/transcribe/test", jsonapi.Post(a.db, a.handleTranscribeTest, a.gate()))

	// Image gen connectivity test — same shape as STT but per-provider
	// (each has its own models URL convention). Validates the API key
	// is recognized; doesn't actually generate an image (which would
	// cost money on every test click).
	sub.HandleFunc("/api/image-gen/test", jsonapi.Post(a.db, a.handleImageGenTest, a.gate()))

	// Speech (TTS): spoken replies to voice notes. Same GET/POST shape as
	// transcription; the test speaks a sentence rather than probing /models,
	// because a speech server that lists models can still refuse the voice.
	sub.HandleFunc("/api/speech", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleSpeech, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleSpeechSave, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/speech/test", jsonapi.Post(a.db, a.handleSpeechTest, a.gate()))
	sub.HandleFunc("/api/speech/voices", jsonapi.Get(a.db, a.handleSpeechVoices, a.gate()))
}

// handleEmbeddings serves /api/embeddings; registerMediaRoutes says what it is for.
func (a *AdminApp) handleEmbeddings(c *jsonapi.Call) (any, error) {
	var cfg EmbeddingConfig
	if a.db != nil {
		a.db.Get(EmbeddingTable, "current", &cfg)
	}
	// Every config stored before peers existed has a blank Provider. Report
	// it as "local" so the dropdown preselects the right option instead of
	// opening on nothing, and so a ShowWhen testing provider:local matches.
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = EmbeddingProviderLocal
	}
	cfg.APIKey = maskSecret(cfg.APIKey)
	return cfg, nil
}

// handleEmbeddingsSave persists the embeddings config and reinstalls it live.
func (a *AdminApp) handleEmbeddingsSave(c *jsonapi.Call, req EmbeddingConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedEmbeddingConfig().APIKey)
	// A peer selection carries no endpoint of its own — the manual
	// fields are hidden while one is picked. Resolve it into a complete,
	// ordinary config here so everything downstream (Embed,
	// EmbedVersion, the vector store) stays peer-unaware.
	resolved, err := ResolveEmbeddingProvider(req)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(EmbeddingTable, "current", req)
	}
	SetEmbeddingConfig(req)
	adminChange(c.R, activity.Settings, "updated embeddings config (enabled=%v provider=%q endpoint=%q model=%q)",
		req.Enabled, req.Provider, req.Endpoint, req.Model)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleEmbeddingsTest serves /api/embeddings/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleEmbeddingsTest(c *jsonapi.Call, req EmbeddingConfig) (any, error) {
	r := c.R
	req.APIKey = keepSecret(req.APIKey, a.storedEmbeddingConfig().APIKey)
	if !req.Enabled {
		return testResultBody(false, "", "embeddings are disabled: flip the toggle on first"), nil
	}
	// Resolve a peer selection the same way the SAVE path does. Without
	// this the test ran the form's literal fields — and with a peer picked
	// those are hidden and empty, so testing a perfectly good peer failed
	// with "endpoint is required" against a form showing no endpoint field
	// to fill in. A test button that cannot test the thing the form is
	// currently configured for is worse than no test button.
	resolved, err := ResolveEmbeddingProvider(req)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	req = resolved
	if req.Endpoint == "" {
		return testResultBody(false, "", "endpoint is required"), nil
	}
	// Temporarily swap in the form's working config for this one call
	// without persisting. Restore on exit so a failed test doesn't
	// poison live state.
	prev := GetEmbeddingConfig()
	SetEmbeddingConfig(req)
	defer SetEmbeddingConfig(prev)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	vec, err := Embed(ctx, "hello from gohort admin connectivity test")
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	modelLabel := req.Model
	if modelLabel == "" {
		modelLabel = "server default"
	}
	// Name WHERE it embedded. The test now has two possible meanings, and a
	// bare "OK" would not distinguish a working peer from a local embedder
	// that answered because the peer selection never took effect.
	where := "this instance"
	if p, ok := PeerFromProvider(req.Provider); ok {
		where = "peer " + p.Name
		if p.Instance != "" {
			where += " (" + p.Instance + ")"
		}
	}
	return testResultBody(true, fmt.Sprintf("OK: %d-dim embedding from %s via %s", len(vec), modelLabel, where), ""), nil
}

// handleEmbeddingModels serves /api/embeddings/models; registerMediaRoutes says what it is for.
func (a *AdminApp) handleEmbeddingModels(c *jsonapi.Call) (any, error) {
	r := c.R
	var cfg EmbeddingConfig
	if a.db != nil {
		a.db.Get(EmbeddingTable, "current", &cfg)
	}
	if cfg.Endpoint == "" {
		return []map[string]string{}, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	// Try OpenAI-compat /models first.
	base := strings.TrimRight(cfg.Endpoint, "/")
	names := probeEmbeddingModels(ctx, base+"/models", cfg.APIKey, "openai")
	if len(names) == 0 {
		// Fall back to Ollama /tags. Strip /v1 if present so we
		// don't end up with /v1/tags (Ollama only serves
		// /api/tags, never under /v1). The base already includes
		// /api for canonical Ollama configs.
		tagsBase := strings.TrimSuffix(base, "/v1")
		names = probeEmbeddingModels(ctx, tagsBase+"/tags", cfg.APIKey, "ollama")
	}
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]string{"id": n, "name": n, "value": n})
	}
	return out, nil
}

// handleTranscribe serves /api/transcribe; registerMediaRoutes says what it is for.
func (a *AdminApp) handleTranscribe(c *jsonapi.Call) (any, error) {
	cfg := a.storedTranscribeConfig()
	cfg.APIKey = maskSecret(cfg.APIKey)
	return cfg, nil
}

// handleTranscribeSave persists the transcription config and reinstalls it
// live.
func (a *AdminApp) handleTranscribeSave(c *jsonapi.Call, req TranscribeConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedTranscribeConfig().APIKey)
	// A peer selection carries no endpoint of its own — the manual
	// fields are hidden while one is picked. Resolve it into a
	// complete, ordinary config here so everything downstream
	// (Transcribe, the transcribe tool, inbound voice notes) stays
	// peer-unaware. Same shape as the embeddings save above.
	resolved, err := ResolveTranscribeProvider(req, req.Provider)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(TranscribeTable, "current", req)
	}
	SetTranscribeConfig(req)
	adminChange(c.R, activity.Settings, "updated transcribe config (enabled=%v endpoint=%q model=%q)",
		req.Enabled, req.Endpoint, req.Model)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSpeech serves GET /api/speech; registerMediaRoutes says what it is for.
func (a *AdminApp) handleSpeech(c *jsonapi.Call) (any, error) {
	cfg := a.storedSpeechConfig()
	cfg.APIKey = maskSecret(cfg.APIKey)
	return cfg, nil
}

// handleSpeechSave persists the speech config and installs it live.
func (a *AdminApp) handleSpeechSave(c *jsonapi.Call, req media.SpeechConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedSpeechConfig().APIKey)
	if a.db != nil {
		a.db.Set(media.SpeechTable, "current", req)
	}
	media.SetSpeechConfig(req)
	adminChange(c.R, activity.Settings, "updated speech config (enabled=%v endpoint=%q model=%q voice=%q)",
		req.Enabled, req.Endpoint, req.Model, req.Voice)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSpeechTest speaks a sentence with the form as typed, saved or not,
// and hands the clip back for the page to play: the only test that proves
// the model and voice are ones the server takes, and the way to hear a voice
// before choosing it.
func (a *AdminApp) handleSpeechTest(c *jsonapi.Call, req media.SpeechConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedSpeechConfig().APIKey)
	if !req.Enabled {
		return testResultBody(false, "", "speech is disabled: flip the toggle on first"), nil
	}
	// A minute: a CPU-only server loads its model on the first request.
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Minute)
	defer cancel()
	audio, ext, err := media.SpeakWith(ctx, req, media.PreviewSentence)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	body := testResultBody(true, fmt.Sprintf("Spoke a sample: %d KB of %s audio.",
		(len(audio)+1023)/1024, strings.TrimPrefix(ext, ".")), "")
	body["audio"] = "data:" + media.SpeechMIME(ext) + ";base64," + base64.StdEncoding.EncodeToString(audio)
	return body, nil
}

// handleSpeechVoices lists the voices the configured server offers, for the
// Voice field's chips. A server without a voice list gets no chips, and the
// field is typed by hand.
func (a *AdminApp) handleSpeechVoices(c *jsonapi.Call) (any, error) {
	return media.VoiceChips(media.SpeechVoices(c.R.Context(), a.storedSpeechConfig())), nil
}

// handleImageGen serves /api/image-gen; registerMediaRoutes says what it is for.
func (a *AdminApp) handleImageGen(c *jsonapi.Call) (any, error) {
	var provider, key string
	if a.db != nil {
		a.db.Get(ImageTable, "provider", &provider)
		a.db.Get(ImageTable, "api_key", &key)
	}
	if provider == "" {
		provider = "gemini"
	}
	return map[string]any{"provider": provider, "api_key": maskSecret(key)}, nil
}

// handleImageGenSave persists the image-generation provider and key.
func (a *AdminApp) handleImageGenSave(c *jsonapi.Call, req imageGenConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedString(ImageTable, "api_key"))
	if a.db != nil {
		a.db.Set(ImageTable, "provider", req.Provider)
		a.db.Set(ImageTable, "api_key", req.APIKey)
	}
	adminChange(c.R, activity.Settings, "updated image-gen config (provider=%q)",
		req.Provider)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleTranscribeTest serves /api/transcribe/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleTranscribeTest(c *jsonapi.Call, req TranscribeConfig) (any, error) {
	r := c.R
	req.APIKey = keepSecret(req.APIKey, a.storedTranscribeConfig().APIKey)
	if !req.Enabled {
		return testResultBody(false, "", "transcription is disabled: flip the toggle on first"), nil
	}
	// Resolve a peer selection the same way the SAVE path does. Without
	// this the test ran the form's literal fields — and with a peer picked
	// those are hidden and empty, so testing a perfectly good peer failed
	// with "endpoint is required" against a form showing no endpoint field
	// to fill in. Exactly the bug the embeddings test button already had.
	resolved, rerr := ResolveTranscribeProvider(req, req.Provider)
	if rerr != nil {
		return testResultBody(false, "", rerr.Error()), nil
	}
	req = resolved
	if req.Endpoint == "" {
		return testResultBody(false, "", "endpoint is required"), nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// A peer is probed through its MANIFEST rather than the GET probe
	// below. The peer base serves exactly one path — /audio/transcriptions
	// — so /models would 404 and the root fallback would land on the far
	// side's web UI and report a cheerful success for a link that cannot
	// transcribe. The manifest answers the questions that actually matter
	// (is it reachable, is the key good, is transcribe granted, does that
	// instance still have STT configured) and says which one failed.
	if p, isPeer := PeerFromProvider(req.Provider); isPeer {
		ok, msg, errMsg := peerTranscribeTestResult(ctx, p)
		return testResultBody(ok, msg, errMsg), nil
	}
	// probeURL: GET with optional bearer header. Returns the response status
	// or any transport error. Closing the body inline so callers don't have to.
	probeURL := func(url string) (int, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return 0, err
		}
		if req.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
		}
		resp, err := (&http.Client{}).Do(httpReq)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	// Try /models first (OpenAI-compatible servers expose this and a
	// 200 also validates the bearer key). Fall back to the endpoint
	// root for servers like whisper.cpp that only expose the
	// transcription path and serve an HTML index at /.
	base := strings.TrimRight(req.Endpoint, "/")
	modelsURL := base + "/models"
	status, err := probeURL(modelsURL)
	if err != nil {
		return testResultBody(false, "", "reach failed: "+err.Error()), nil
	}
	switch {
	case status >= 200 && status < 300:
		return testResultBody(true, fmt.Sprintf("Endpoint reachable + /models OK (HTTP %d)", status), ""), nil
	case status == 401 || status == 403:
		return testResultBody(false, "", fmt.Sprintf("HTTP %d: endpoint reached but rejected the API key", status)), nil
	}
	// /models 404/405 → fall back to a plain GET on the endpoint root.
	// Strip any trailing /v1 (or /api) so we hit the actual host root.
	rootBase := base
	for _, suffix := range []string{"/v1", "/api"} {
		if strings.HasSuffix(rootBase, suffix) {
			rootBase = rootBase[:len(rootBase)-len(suffix)]
			break
		}
	}
	rootStatus, err := probeURL(rootBase + "/")
	if err != nil {
		return testResultBody(false, "", fmt.Sprintf("HTTP %d at %s, and root probe failed: %s", status, modelsURL, err.Error())), nil
	}
	if rootStatus >= 200 && rootStatus < 500 {
		return testResultBody(true, fmt.Sprintf("Endpoint reachable (HTTP %d at root; %d at /models: server doesn't expose /models, fine for whisper.cpp)", rootStatus, status), ""), nil
	}
	return testResultBody(false, "", fmt.Sprintf("HTTP %d at root, HTTP %d at /models", rootStatus, status)), nil
}

// handleImageGenTest serves /api/image-gen/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleImageGenTest(c *jsonapi.Call, req imageGenConfig) (any, error) {
	r := c.R
	if req.Provider == "" || req.Provider == "none" {
		return testResultBody(false, "", "pick a provider first"), nil
	}
	// Connector-backed provider (rest_image: ComfyUI / A1111 / custom): it
	// carries its own SecureAPI credential, so there's no API key to probe
	// here. Report that it's live rather than falling into the built-in
	// gemini/openai key test (which would reject the name as "unknown").
	if ImageBackendRegistered(req.Provider) {
		return testResultBody(true, "Connector backend “"+req.Provider+"” is approved and active. It uses its own credential: generate an image to verify it end to end.", ""), nil
	}
	// Fall back to the key of an LLM that uses the SAME provider, worker
	// first then lead: the rule the GenerateImage runtime uses
	// (geminiAPIKey / openAIAPIKey). This used to take the worker's key
	// whatever its provider, so a test could pass or fail on a key the
	// real call would never use.
	key := keepSecret(req.APIKey, a.storedString(ImageTable, "api_key"))
	if key == "" && a.db != nil && (req.Provider == "gemini" || req.Provider == "openai") {
		for _, table := range []string{LLMTable, LeadLLMTable} {
			var provider, k string
			a.db.Get(table, "provider", &provider)
			a.db.Get(table, "api_key", &k)
			if provider == req.Provider && k != "" {
				key = k
				break
			}
		}
	}
	if key == "" {
		return testResultBody(false, "", "no API key: set one here, or set the matching LLM provider's key"), nil
	}
	var url string
	var authHeader, authPrefix string
	switch req.Provider {
	case "openai":
		url = "https://api.openai.com/v1/models"
		authHeader, authPrefix = "Authorization", "Bearer "
	case "gemini":
		// Gemini takes the key as ?key= rather than a header.
		url = "https://generativelanguage.googleapis.com/v1beta/models?key=" + key
	default:
		return testResultBody(false, "", "unknown provider: "+req.Provider), nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	if authHeader != "" {
		httpReq.Header.Set(authHeader, authPrefix+key)
	}
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		// Gemini's key rides in the URL, and a request that fails names its
		// URL: said as it is, the stored key the form masks would come back
		// in the error.
		return testResultBody(false, "", "reach failed: "+strings.ReplaceAll(err.Error(), key, "<key>")), nil
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return testResultBody(true, fmt.Sprintf("%s reachable + key accepted (HTTP %d)", req.Provider, resp.StatusCode), ""), nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return testResultBody(false, "", fmt.Sprintf("HTTP %d: %s rejected the API key", resp.StatusCode, req.Provider)), nil
	default:
		return testResultBody(false, "", fmt.Sprintf("HTTP %d from %s", resp.StatusCode, req.Provider)), nil
	}
}

// imageGenConfig is the image-generation form: its provider and key.
type imageGenConfig struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// probeEmbeddingModels fetches and parses a model list from an
// embedding endpoint's discovery URL. shape controls how to interpret
// the response body:
//   - "openai":  {"data": [{"id": "..."}, ...]}  (OpenAI, vLLM, llama.cpp /v1/models, hf-tei /models)
//   - "ollama":  {"models": [{"name": "..."}, ...]}  (Ollama /api/tags)
//
// Returns an empty slice on any error so the caller can quietly fall
// back to the next probe form without surfacing an error to the UI.
func probeEmbeddingModels(ctx context.Context, url, apiKey, shape string) []string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	switch shape {
	case "openai":
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out
	case "ollama":
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Models))
		for _, m := range body.Models {
			if m.Name != "" {
				out = append(out, m.Name)
			}
		}
		return out
	}
	return nil
}

// storedEmbeddingConfig / storedTranscribeConfig read the saved config, so a
// posted secretUnchanged can be swapped back for the stored key.
func (a *AdminApp) storedEmbeddingConfig() EmbeddingConfig {
	var cfg EmbeddingConfig
	if a.db != nil {
		a.db.Get(EmbeddingTable, "current", &cfg)
	}
	return cfg
}

func (a *AdminApp) storedSpeechConfig() media.SpeechConfig {
	var cfg media.SpeechConfig
	if a.db != nil {
		a.db.Get(media.SpeechTable, "current", &cfg)
	}
	return cfg
}

func (a *AdminApp) storedTranscribeConfig() TranscribeConfig {
	var cfg TranscribeConfig
	if a.db != nil {
		a.db.Get(TranscribeTable, "current", &cfg)
	}
	return cfg
}
