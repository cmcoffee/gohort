package admin

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
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

	// Audio transcription (STT): GET/POST. POST persists + reinstalls
	// the live TranscribeConfig so the next Transcribe() call uses the
	// connector or peer chosen, without a restart.
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

	// STT test: the form as it stands, saved or not. A connector
	// transcribes a second of a tone; a peer is checked through its
	// manifest.
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
	cfg := a.storedEmbeddingConfig()
	// Where embeddings are made is the connector or peer named: its key is
	// its own, never shown here.
	cfg.APIKey = ""
	return cfg, nil
}

// handleEmbeddingsSave persists the embeddings config and reinstalls it live.
func (a *AdminApp) handleEmbeddingsSave(c *jsonapi.Call, req EmbeddingConfig) (any, error) {
	req.APIKey = ""
	// The form names a connector or a peer and nothing else: resolve it into
	// a complete config here, its endpoint and model filled in, so everything
	// downstream (Embed, EmbedVersion, the vector store) reads one shape.
	resolved, err := resolveEmbedding(req)
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
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "embeddings are disabled: flip the toggle on first"), nil
	}
	// Resolve a peer selection the same way the SAVE path does. Without
	// this the test ran the form's literal fields — and with a peer picked
	// those are hidden and empty, so testing a perfectly good peer failed
	// with "endpoint is required" against a form showing no endpoint field
	// to fill in. A test button that cannot test the thing the form is
	// currently configured for is worse than no test button.
	resolved, err := resolveEmbedding(req)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	req = resolved
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
	// Name WHERE it embedded, so a working connector is not mistaken for a
	// peer that answered because the selection never took effect.
	where := req.Provider
	if p, ok := PeerFromProvider(req.Provider); ok {
		where = "peer " + p.Name
		if p.Instance != "" {
			where += " (" + p.Instance + ")"
		}
	} else if name, ok := strings.CutPrefix(req.Provider, media.ServiceProviderPrefix); ok {
		where = "the service " + name
	}
	return testResultBody(true, fmt.Sprintf("OK: %d-dim embedding from %s via %s", len(vec), modelLabel, where), ""), nil
}

// handleTranscribe serves /api/transcribe; registerMediaRoutes says what it is for.
func (a *AdminApp) handleTranscribe(c *jsonapi.Call) (any, error) {
	cfg := a.storedTranscribeConfig()
	// What transcribes is the connector or peer named: its key is its own,
	// never shown here.
	cfg.APIKey = ""
	return cfg, nil
}

// handleTranscribeSave persists the transcription config and reinstalls it
// live.
func (a *AdminApp) handleTranscribeSave(c *jsonapi.Call, req TranscribeConfig) (any, error) {
	req.APIKey = ""
	// The form names a connector or a peer and nothing else: resolve it into
	// a complete config here, so everything downstream (Transcribe, the
	// transcribe tool, inbound voice notes) reads one shape. Same shape as
	// the embeddings save above.
	resolved, err := resolveTranscription(req)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(TranscribeTable, "current", req)
	}
	SetTranscribeConfig(req)
	adminChange(c.R, activity.Settings, "updated transcribe config (enabled=%v provider=%q model=%q)",
		req.Enabled, req.Provider, req.Model)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// resolveTranscription is ResolveTranscribeProvider with a service checked
// once resolved, and its model taken from the service's spec: the model a
// peer borrowing transcription from here is told it gets.
func resolveTranscription(req TranscribeConfig) (TranscribeConfig, error) {
	resolved, err := ResolveTranscribeProvider(req, req.Provider)
	if err != nil {
		return req, err
	}
	if err := serviceProviderOK(resolved.Provider, "speech.transcribe"); err != nil {
		return req, err
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(resolved.Provider), media.ServiceProviderPrefix); ok {
		c, _ := GetConnector(RootDB, name)
		resolved.Model = service.Default(c, "speech.transcribe", "model")
	}
	return resolved, nil
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
	// A peer selection carries no endpoint of its own: resolve it into an
	// ordinary config, so everything that speaks stays peer-unaware.
	if err := serviceProviderOK(req.Provider, "speech.synthesize"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	resolved, err := ResolveSpeechProvider(req, req.Provider)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
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
	// A peer picked in the form is resolved as the save resolves it, so the
	// preview is spoken by the peer: the whole link, proven by hearing it.
	resolved, rerr := ResolveSpeechProvider(req, req.Provider)
	if rerr != nil {
		return testResultBody(false, "", rerr.Error()), nil
	}
	req = resolved
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
	cfg := a.storedSpeechConfig()
	// A peer's voices come from the peer, with its current credential.
	if resolved, err := ResolveSpeechProvider(cfg, cfg.Provider); err == nil {
		cfg = resolved
	}
	return media.VoiceChips(media.SpeechVoices(c.R.Context(), cfg)), nil
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
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "transcription is disabled: flip the toggle on first"), nil
	}
	// Resolved as the save resolves it, so the test runs what the form is
	// set to, saved or not.
	resolved, err := resolveTranscription(req)
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	req = resolved
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Minute)
	defer cancel()
	// A peer is probed through its MANIFEST: is it reachable, is the key
	// good, is transcribe granted, does that instance still transcribe. Each
	// says which one failed, where sending audio would only say it failed.
	if p, isPeer := PeerFromProvider(req.Provider); isPeer {
		ok, msg, errMsg := peerTranscribeTestResult(ctx, p)
		return testResultBody(ok, msg, errMsg), nil
	}
	// A connector transcribes a second of a tone, through the path every
	// transcription takes. A tone has no words: any answer, none included,
	// means it took the audio and replied in the shape its spec reads.
	text, err := TranscribeWith(ctx, req, service.SampleTone(), "tone.wav")
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	heard := "no words, as expected of a tone"
	if r := []rune(text); len(r) > 80 {
		heard = fmt.Sprintf("%q...", string(r[:80]))
	} else if text != "" {
		heard = fmt.Sprintf("%q", text)
	}
	name := strings.TrimPrefix(req.Provider, media.ServiceProviderPrefix)
	return testResultBody(true, "OK: the service "+name+" transcribed a second of a tone and heard "+heard, ""), nil
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

// resolveEmbedding is ResolveEmbeddingProvider with a service checked first,
// and its model taken from the service's spec: the stored model names the
// space the vectors are in, and the service is asked for that model by name.
func resolveEmbedding(req EmbeddingConfig) (EmbeddingConfig, error) {
	resolved, err := ResolveEmbeddingProvider(req)
	if err != nil {
		return req, err
	}
	// Checked once resolved: a form that sent no provider keeps the stored
	// one, which has to be as usable as one picked.
	if err := serviceProviderOK(resolved.Provider, "embed.text"); err != nil {
		return req, err
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(resolved.Provider), media.ServiceProviderPrefix); ok {
		c, _ := GetConnector(RootDB, name)
		resolved.Model = service.Default(c, "embed.text", "model")
	}
	return resolved, nil
}

// serviceProviderOK refuses a provider that names a service connector which
// is missing, not approved, or does not provide capability. Anything else
// (this instance, a peer) is left to its own resolution.
func serviceProviderOK(provider, capability string) error {
	name, ok := strings.CutPrefix(strings.TrimSpace(provider), media.ServiceProviderPrefix)
	if !ok {
		return nil
	}
	c, found := GetConnector(RootDB, name)
	switch {
	case !found:
		return fmt.Errorf("no connector named %q", name)
	case !service.Provides(c, capability):
		return fmt.Errorf("connector %q does not provide %s", name, capability)
	case !c.Approved:
		return fmt.Errorf("connector %q is not approved yet: approve it in Admin > Extensions > Connectors first", name)
	}
	return nil
}

// serviceProviderOptions are the approved service connectors providing
// capability, as choices beside this instance and the peers.
func serviceProviderOptions(capability, verb string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Approved && service.Provides(c, capability) {
			out = append(out, ui.SelectOption{Value: media.ServiceProviderPrefix + c.Name, Label: "Service: " + c.Name,
				Help: verb + " with the " + c.Name + " connector: " + ConnectorSummary(c) + "."})
		}
	}
	return out
}
