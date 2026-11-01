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

	// Music: the music connectors offered to agents as tools.
	sub.HandleFunc("/api/music", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleMusic, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleMusicSave, a.gate()),
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
	// transcription; the test speaks a sentence, because a connector that
	// answers can still refuse the voice.
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
	return withValues(cfg, chosenService(cfg.Provider), "embed.text"), nil
}

// handleEmbeddingsSave persists the embeddings config and reinstalls it live.
func (a *AdminApp) handleEmbeddingsSave(c *jsonapi.Call, form withSettings[EmbeddingConfig]) (any, error) {
	req, conn := form.Form, chosenService(form.Form.Provider)
	req.APIKey = ""
	// The form names a connector or a peer and nothing else: resolve it into
	// a complete config here, its endpoint and model filled in, so everything
	// downstream (Embed, EmbedVersion, the vector store) reads one shape.
	resolved, err := resolveEmbedding(req, form.Settings.values(conn, "embed.text"))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(conn, "embed.text"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(EmbeddingTable, "current", req)
	}
	SetEmbeddingConfig(req)
	settleTieIn(req.Provider)
	adminChange(c.R, activity.Settings, "updated embeddings config (enabled=%v provider=%q endpoint=%q model=%q)",
		req.Enabled, req.Provider, req.Endpoint, req.Model)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleEmbeddingsTest serves /api/embeddings/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleEmbeddingsTest(c *jsonapi.Call, form withSettings[EmbeddingConfig]) (any, error) {
	r := c.R
	req, conn := form.Form, chosenService(form.Form.Provider)
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
	resolved, err := resolveEmbedding(req, form.Settings.values(conn, "embed.text"))
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
	ctx, end := previewTieIn(form.Settings.testContext(r.Context(), conn, "embed.text"), req.Provider)
	defer end()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
	return withValues(cfg, chosenService(cfg.Provider), "speech.transcribe"), nil
}

// handleTranscribeSave persists the transcription config and reinstalls it
// live.
func (a *AdminApp) handleTranscribeSave(c *jsonapi.Call, form withSettings[TranscribeConfig]) (any, error) {
	req, conn := form.Form, chosenService(form.Form.Provider)
	req.APIKey = ""
	// The form names a connector or a peer and nothing else: resolve it into
	// a complete config here, so everything downstream (Transcribe, the
	// transcribe tool, inbound voice notes) reads one shape. Same shape as
	// the embeddings save above.
	resolved, err := resolveTranscription(req, form.Settings.values(conn, "speech.transcribe"))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(conn, "speech.transcribe"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(TranscribeTable, "current", req)
	}
	SetTranscribeConfig(req)
	settleTieIn(req.Provider)
	adminChange(c.R, activity.Settings, "updated transcribe config (enabled=%v provider=%q model=%q)",
		req.Enabled, req.Provider, req.Model)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// resolveTranscription is ResolveTranscribeProvider with a service checked
// once resolved, and its model taken from the service's spec: the model a
// peer borrowing transcription from here is told it gets.
func resolveTranscription(req TranscribeConfig, values map[string]string) (TranscribeConfig, error) {
	resolved, err := ResolveTranscribeProvider(req, req.Provider)
	if err != nil {
		return req, err
	}
	if err := serviceProviderOK(resolved.Provider, "speech.transcribe"); err != nil {
		return req, err
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(resolved.Provider), media.ServiceProviderPrefix); ok {
		c, _ := GetConnector(RootDB, name)
		resolved.Model = service.DefaultWith(c, "speech.transcribe", "model", values)
	}
	return resolved, nil
}

// handleSpeech serves GET /api/speech; registerMediaRoutes says what it is for.
func (a *AdminApp) handleSpeech(c *jsonapi.Call) (any, error) {
	cfg := a.storedSpeechConfig()
	// What speaks is the connector or peer named: its key is its own, never
	// shown here.
	cfg.APIKey = ""
	return withValues(cfg, chosenService(cfg.Provider), "speech.synthesize"), nil
}

// handleSpeechSave persists the speech config and installs it live.
func (a *AdminApp) handleSpeechSave(c *jsonapi.Call, form withSettings[media.SpeechConfig]) (any, error) {
	req, conn := form.Form, chosenService(form.Form.Provider)
	req.APIKey = ""
	// The form names a connector or a peer and a voice: resolve it into a
	// complete config, so everything that speaks reads one shape.
	resolved, err := resolveSpeech(req, form.Settings.values(conn, "speech.synthesize"))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(conn, "speech.synthesize"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req = resolved
	if a.db != nil {
		a.db.Set(media.SpeechTable, "current", req)
	}
	media.SetSpeechConfig(req)
	settleTieIn(req.Provider)
	adminChange(c.R, activity.Settings, "updated speech config (enabled=%v provider=%q model=%q voice=%q)",
		req.Enabled, req.Provider, req.Model, req.Voice)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// resolveSpeech is ResolveSpeechProvider with a service checked once
// resolved, and its model taken from the service's spec: the model a peer
// borrowing speech from here is told it gets.
func resolveSpeech(req media.SpeechConfig, values map[string]string) (media.SpeechConfig, error) {
	resolved, err := ResolveSpeechProvider(req, req.Provider)
	if err != nil {
		return req, err
	}
	if err := serviceProviderOK(resolved.Provider, "speech.synthesize"); err != nil {
		return req, err
	}
	if name, ok := strings.CutPrefix(strings.TrimSpace(resolved.Provider), media.ServiceProviderPrefix); ok {
		c, _ := GetConnector(RootDB, name)
		resolved.Model = service.DefaultWith(c, "speech.synthesize", "model", values)
	}
	return resolved, nil
}

// handleSpeechTest speaks a sentence with the form as typed, saved or not,
// and hands the clip back for the page to play: the only test that proves
// the voice is one the connector or peer takes, and the way to hear a voice
// before choosing it.
func (a *AdminApp) handleSpeechTest(c *jsonapi.Call, form withSettings[media.SpeechConfig]) (any, error) {
	req, conn := form.Form, chosenService(form.Form.Provider)
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "speech is disabled: flip the toggle on first"), nil
	}
	// Resolved as the save resolves it, so a peer picked in the form speaks
	// the preview: the whole link, proven by hearing it.
	resolved, rerr := resolveSpeech(req, form.Settings.values(conn, "speech.synthesize"))
	if rerr != nil {
		return testResultBody(false, "", rerr.Error()), nil
	}
	req = resolved
	ctx, end := previewTieIn(form.Settings.testContext(c.R.Context(), conn, "speech.synthesize"), req.Provider)
	defer end()
	// A minute: a CPU-only server loads its model on the first request.
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
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

// handleSpeechVoices lists the voices the configured connector or peer
// offers, for the Voice field's chips. One without a voice list gets no
// chips, and the voice is typed.
func (a *AdminApp) handleSpeechVoices(c *jsonapi.Call) (any, error) {
	cfg := a.storedSpeechConfig()
	// A peer's voices come from the peer, with its current credential.
	if resolved, err := ResolveSpeechProvider(cfg, cfg.Provider); err == nil {
		cfg = resolved
	}
	ctx, end := previewTieIn(c.R.Context(), cfg.Provider)
	defer end()
	return media.VoiceChips(media.SpeechVoices(ctx, cfg)), nil
}

// handleImageGen serves /api/image-gen; registerMediaRoutes says what it is for.
func (a *AdminApp) handleImageGen(c *jsonapi.Call) (any, error) {
	provider := a.storedString(ImageTable, "provider")
	if provider == "" {
		provider = "none"
	}
	agents := []string{}
	if a.db != nil {
		a.db.Get(ImageTable, "agents", &agents)
	}
	out := map[string]any{"provider": provider, "agents": agents}
	for k, v := range settingsValues(provider, "image.generate") {
		out[k] = v
	}
	return out, nil
}

// handleImageGenSave persists the image connector and the others offered to
// agents, refusing a name that is not a connector making pictures: saved, it
// would leave every picture failing on a connector that is not there. Naming
// one puts it to use.
func (a *AdminApp) handleImageGenSave(c *jsonapi.Call, form withSettings[imageGenConfig]) (any, error) {
	req := form.Form
	req.Provider = strings.TrimSpace(req.Provider)
	names := []string{}
	if req.Provider != "" && req.Provider != "none" {
		names = append(names, req.Provider)
	}
	var agents []string
	for _, n := range req.Agents {
		if n = strings.TrimSpace(n); n != "" && n != req.Provider {
			agents = append(agents, n)
		}
	}
	for _, n := range append(names, agents...) {
		if !imageConnector(n) {
			return nil, jsonapi.BadRequest("no connector named " + n + " makes pictures: add one under Admin > Extensions > Connectors or Templates first")
		}
	}
	if len(names) > 0 {
		if err := form.Settings.save(req.Provider, "image.generate"); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	}
	if a.db != nil {
		a.db.Set(ImageTable, "provider", req.Provider)
		a.db.Set(ImageTable, "agents", agents)
	}
	settleTieIn(append(names, agents...)...)
	offered := ""
	if len(agents) > 0 {
		offered = ", also offered to agents: " + strings.Join(agents, ", ")
	}
	adminChange(c.R, activity.Settings, "updated image-gen config (provider=%q%s)", req.Provider, offered)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// imageConnector reports whether name is a connector that makes or edits
// pictures: an image backend, or a service giving image.generate or
// image.edit.
func imageConnector(name string) bool {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return false
	}
	return c.Kind == RestImageConnectorKind || (c.Kind == service.Kind && (service.Provides(c, "image.generate") || service.Provides(c, "image.edit")))
}

// handleTranscribeTest serves /api/transcribe/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleTranscribeTest(c *jsonapi.Call, form withSettings[TranscribeConfig]) (any, error) {
	req, conn := form.Form, chosenService(form.Form.Provider)
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "transcription is disabled: flip the toggle on first"), nil
	}
	// Resolved as the save resolves it, so the test runs what the form is
	// set to, saved or not.
	resolved, err := resolveTranscription(req, form.Settings.values(conn, "speech.transcribe"))
	if err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	req = resolved
	ctx, end := previewTieIn(form.Settings.testContext(c.R.Context(), conn, "speech.transcribe"), req.Provider)
	defer end()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
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
// It does not render: a picture can be billed. It says whether the chosen
// connector makes pictures and is in use, and where its own Test renders one.
func (a *AdminApp) handleImageGenTest(c *jsonapi.Call, req imageGenConfig) (any, error) {
	if req.Provider == "" || req.Provider == "none" {
		return testResultBody(false, "", "pick a connector first"), nil
	}
	if !imageConnector(req.Provider) && !ImageBackendRegistered(req.Provider) {
		return testResultBody(false, "", "no connector named "+req.Provider+" makes pictures"), nil
	}
	if !ImageBackendRegistered(req.Provider) {
		return testResultBody(false, "", req.Provider+" makes pictures but is not in use yet: save here with it chosen to put it to use"), nil
	}
	return testResultBody(true, "The connector "+req.Provider+" is in use. Its Test under Admin > Extensions > Connectors renders a picture end to end.", ""), nil
}

// imageGenConfig is the image-generation form: the connector chosen.
type imageGenConfig struct {
	Provider string   `json:"provider"`
	Agents   []string `json:"agents"`
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
func resolveEmbedding(req EmbeddingConfig, values map[string]string) (EmbeddingConfig, error) {
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
		resolved.Model = service.DefaultWith(c, "embed.text", "model", values)
	}
	return resolved, nil
}

// serviceProviderOK refuses a provider that names a service connector which
// is missing or does not provide capability. Anything else (this instance, a
// peer) is left to its own resolution. Naming one here is what puts it to
// use: the page's save settles it (settleTieIn).
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
	}
	return nil
}

// serviceProviderOptions are the service connectors providing capability,
// as choices beside this instance and the peers: every one, since choosing
// it here is what puts it to use. One changed since it was put to use says
// so: saving the page with it chosen accepts the change.
func serviceProviderOptions(capability, verb string) []ui.SelectOption {
	var provided, others []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == service.Kind && service.Provides(c, capability) {
			label, help := connectorLabel(c), verb+" with "+c.Title()+": "+ConnectorSummary(c)+"."
			if c.Provided {
				help = verb + " with " + c.Title() + ", provided with gohort: give its key and settings below, then save."
			}
			if ch, ok := service.PendingChange(RootDB, c.Name); ok {
				label += " (changed, waiting to be accepted)"
				help += " " + changedNote(ch)
			}
			opt := ui.SelectOption{Value: media.ServiceProviderPrefix + c.Name, Label: label, Help: help}
			if c.Provided {
				provided = append(provided, opt)
			} else {
				others = append(others, opt)
			}
		}
	}
	return append(provided, others...)
}

// changedNote says who changed a connector and how to accept it.
func changedNote(ch service.ConnectorChange) string {
	by := ch.By
	if by == "" {
		by = "someone who is not an administrator"
	}
	return "Changed by " + by + " on " + ch.At.Format("2006-01-02 15:04") + " since it was put to use, so it is out of use: read its spec under Connectors, then save this page with it chosen to accept the change."
}

// settleTieIn settles what is in use after a page names connectors, given
// as provider settings ("service:<name>") or names: the administrator saving
// the page accepts them as they are.
func settleTieIn(named ...string) {
	var names []string
	for _, n := range named {
		n = strings.TrimSpace(n)
		if rest, ok := strings.CutPrefix(n, media.ServiceProviderPrefix); ok {
			n = rest
		}
		if n != "" && !strings.Contains(n, ":") {
			names = append(names, n)
		}
	}
	service.Settle(RootDB, names...)
}

// musicConfig is the Music tie-in: the music connectors offered to agents.
type musicConfig struct {
	Agents []string `json:"agents"`
}

func (a *AdminApp) handleMusic(c *jsonapi.Call) (any, error) {
	cfg := musicConfig{Agents: []string{}}
	if a.db != nil {
		a.db.Get(service.MusicTable, "agents", &cfg.Agents)
	}
	return cfg, nil
}

// handleMusicSave offers the music connectors chosen to agents, as tools of
// their own (generate_music_<connector>), which puts them to use.
func (a *AdminApp) handleMusicSave(c *jsonapi.Call, req musicConfig) (any, error) {
	var agents []string
	for _, n := range req.Agents {
		n = strings.TrimSpace(n)
		conn, ok := GetConnector(RootDB, n)
		if !ok || !service.Provides(conn, "music.generate") {
			return nil, jsonapi.BadRequest("no connector named " + n + " makes music")
		}
		agents = append(agents, n)
	}
	if a.db != nil {
		a.db.Set(service.MusicTable, "agents", agents)
	}
	settleTieIn(agents...)
	adminChange(c.R, activity.Settings, "music offered to agents: %s", strings.Join(agents, ", "))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// musicOptions are the connectors that make music.
func musicOptions() []ui.SelectOption {
	var out []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == service.Kind && service.Provides(c, "music.generate") {
			label := c.Name
			if _, waiting := service.PendingChange(RootDB, c.Name); waiting {
				label += " (changed, waiting to be accepted)"
			}
			out = append(out, ui.SelectOption{Value: c.Name, Label: label, Help: ConnectorSummary(c)})
		}
	}
	return out
}

// previewTieIn lets the settings page's own test reach the connector its
// form names (a provider setting, "service:<name>") before a save puts it
// to use: under the returned context; end ends it.
func previewTieIn(ctx context.Context, provider string) (context.Context, func()) {
	name, ok := strings.CutPrefix(strings.TrimSpace(provider), media.ServiceProviderPrefix)
	if !ok {
		return ctx, func() {}
	}
	return service.Preview(ctx, name)
}

// connectorLabel is how a page's choices show connector c: its display name,
// or, with none, its name marked as a connector.
func connectorLabel(c Connector) string {
	if strings.TrimSpace(c.DisplayName) != "" {
		return c.Title()
	}
	return "Service: " + c.Name
}
