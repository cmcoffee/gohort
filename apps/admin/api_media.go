package admin

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"sort"
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

	// STT test: the form as it stands, saved or not. A connector
	// transcribes a second of a tone; a peer is checked through its
	// manifest.
	sub.HandleFunc("/api/transcribe/test", jsonapi.Post(a.db, a.handleTranscribeTest, a.gate()))

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
	return shownOff(markStale(withValues(cfg, chosenConnection(cfg.Provider), "embed.text"), "provider", ""), cfg.Enabled), nil
}

// handleEmbeddingsSave persists the embeddings config and reinstalls it live.
func (a *AdminApp) handleEmbeddingsSave(c *jsonapi.Call, form withSettings[EmbeddingConfig]) (any, error) {
	turnedOff(&form.Form.Provider, &form.Form.Enabled, a.storedEmbeddingConfig().Provider)
	req, conn := form.Form, chosenConnection(form.Form.Provider)
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
	prev := GetEmbeddingConfig()
	SetEmbeddingConfig(req)
	acceptChanges(req.Provider)
	adminChange(c.R, activity.Settings, "updated embeddings config (enabled=%v provider=%q endpoint=%q model=%q)",
		req.Enabled, req.Provider, req.Endpoint, req.Model)
	// A new model or document prefix leaves every stored vector in the old
	// space, out of semantic search until re-embedded: start that now rather
	// than leave it to a button nobody knows to press.
	if RepairAfterSpaceChange(prev, GetEmbeddingConfig()) {
		return map[string]string{"message": "Saved. Stored documents are being re-embedded for the new model or prefix: progress is under Maintenance, Vector index, where it can be stopped."}, nil
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleEmbeddingsTest serves /api/embeddings/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleEmbeddingsTest(c *jsonapi.Call, form withSettings[EmbeddingConfig]) (any, error) {
	r := c.R
	turnedOff(&form.Form.Provider, &form.Form.Enabled, "")
	req, conn := form.Form, chosenConnection(form.Form.Provider)
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "embeddings are disabled: choose what embeds first"), nil
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
	} else if name, ok := media.ChosenConnection(req.Provider); ok {
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
	return shownOff(markStale(withValues(cfg, chosenConnection(cfg.Provider), "speech.transcribe"), "provider", ""), cfg.Enabled), nil
}

// handleTranscribeSave persists the transcription config and reinstalls it
// live.
func (a *AdminApp) handleTranscribeSave(c *jsonapi.Call, form withSettings[TranscribeConfig]) (any, error) {
	turnedOff(&form.Form.Provider, &form.Form.Enabled, a.storedTranscribeConfig().Provider)
	req, conn := form.Form, chosenConnection(form.Form.Provider)
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
	acceptChanges(req.Provider)
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
	if name, ok := media.ChosenConnection(strings.TrimSpace(resolved.Provider)); ok {
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
	return shownOff(markStale(withValues(cfg, chosenConnection(cfg.Provider), "speech.synthesize"), "provider", ""), cfg.Enabled), nil
}

// handleSpeechSave persists the speech config and installs it live.
func (a *AdminApp) handleSpeechSave(c *jsonapi.Call, form withSettings[media.SpeechConfig]) (any, error) {
	turnedOff(&form.Form.Provider, &form.Form.Enabled, a.storedSpeechConfig().Provider)
	req, conn := form.Form, chosenConnection(form.Form.Provider)
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
	acceptChanges(req.Provider)
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
	if name, ok := media.ChosenConnection(strings.TrimSpace(resolved.Provider)); ok {
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
	turnedOff(&form.Form.Provider, &form.Form.Enabled, "")
	req, conn := form.Form, chosenConnection(form.Form.Provider)
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "speech is disabled: choose what speaks first"), nil
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

// imageBackendRow is one connector on Media Generation's list: an image
// backend, or a music or media connector.
type imageBackendRow struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Role is how Media Generation uses it: "default" (what agents' image
	// tools use unless told otherwise, badged Default), "offered" (agents
	// may choose it by name), or "unused". OfferOnly marks a music or media
	// row, which has no default.
	Role      string `json:"role"`
	IsDefault bool   `json:"is_default,omitempty"`
	OfferOnly bool   `json:"offer_only,omitempty"`
	// Use is its row's switch: "offered" (Enabled, the default among them)
	// or "unused" (Disabled). CanMakeDefault is an enabled image backend
	// that is not the default, which its Make default button makes it.
	Use            string `json:"use,omitempty"`
	CanMakeDefault bool   `json:"can_make_default,omitempty"`
	Does           string `json:"does"`
	Size           string `json:"size"`
	Steps          string `json:"steps,omitempty"`
	// What its row can do, set exactly as a Connectors row's are, so it opens
	// the same editor there and here.
	EditForm bool `json:"edit_form,omitempty"`
	EditSpec bool `json:"edit_spec,omitempty"`
	NeedsFix bool `json:"needs_fix,omitempty"`
	// TemplateUpdate: its template now writes a different spec.
	TemplateUpdate bool `json:"template_update,omitempty"`
	// On Media Generation: the band it sits in (Images, Music, or the kind
	// a media connector makes), and for an image backend that both makes and
	// edits pictures, what it is used for.
	Group    string `json:"group,omitempty"`
	IsImage  bool   `json:"is_image,omitempty"`
	BothWays bool   `json:"both_ways,omitempty"`
	Uses     string `json:"uses,omitempty"`
	// OneJob marks an image backend that does one job, and Job is that job
	// (oneJobOptions), shown greyed where a both-ways one has its choice.
	OneJob bool   `json:"one_job,omitempty"`
	Job    string `json:"job,omitempty"`
	// Stale says why a choice Media Generation holds no longer reads (it
	// names a connection not here, or one named the old way); IsStale gives
	// its row Remove.
	Stale       string `json:"stale,omitempty"`
	IsStale     bool   `json:"is_stale,omitempty"`
	HasSettings bool   `json:"has_settings,omitempty"`
	Testable    bool   `json:"testable,omitempty"`
	TestConfirm string `json:"test_confirm,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// imageRows are the image backends, the default first, then the ones
// offered to agents, then the rest.
func (a *AdminApp) imageRows() []imageBackendRow {
	provider := a.storedString(ImageTable, "provider")
	var agents []string
	if a.db != nil {
		a.db.Get(ImageTable, "agents", &agents)
	}
	rows := []imageBackendRow{}
	for _, conn := range ListConnectors(RootDB) {
		if !imageBackendListed(conn) {
			continue
		}
		o := OutlineImageBackend(conn)
		row := imageBackendRow{Name: conn.Name, Title: rowTitle(conn), Summary: ConnectorSummary(conn),
			Role: "unused", Does: o.Does, Size: o.Size, Steps: o.Steps,
			HasSettings: conn.Kind == service.Kind, LastError: conn.LastError}
		switch {
		case conn.Name == provider:
			row.Role, row.IsDefault = "default", true
		case slices.Contains(agents, conn.Name):
			row.Role = "offered"
		}
		row.EditForm, row.EditSpec, row.NeedsFix = connectorEditors(conn)
		_, row.TemplateUpdate = templateUpdate(conn)
		if service.Testable(conn) {
			row.Testable, row.TestConfirm = true, testConfirm(conn.Name)
		}
		rows = append(rows, row)
	}
	rank := map[string]int{"default": 0, "offered": 1, "unused": 2}
	sort.SliceStable(rows, func(i, j int) bool { return rank[rows[i].Role] < rank[rows[j].Role] })
	return rows
}

// setImageRole changes how Media Generation uses image backend name: role
// "default" makes it what agents' image tools use unless told otherwise,
// keeping the one it replaces offered by name; "offered" lets agents choose
// it by name, and leaves the default the default (Enabled, on the row whose
// switch already reads it); "off" stops using it (the default off leaves no
// default). The
// row's role control sends it; the save is the page's own, so it accepts a change
// waiting on the backend as the page's save does.
func (a *AdminApp) setImageRole(r *http.Request, name, role string) (any, error) {
	if !imageConnector(name) && !ImageBackendRegistered(name) {
		return nil, jsonapi.BadRequest("no image backend named " + name)
	}
	provider := a.storedString(ImageTable, "provider")
	if provider == "none" {
		provider = "" // off, as the old form stored it
	}
	var agents []string
	if a.db != nil {
		a.db.Get(ImageTable, "agents", &agents)
	}
	agents = slices.DeleteFunc(agents, func(n string) bool { return n == name })
	switch role {
	case "default":
		if provider != "" && provider != name && !slices.Contains(agents, provider) {
			agents = append(agents, provider)
		}
		provider = name
	case "offered":
		// The default is enabled already: its switch saying Enabled again
		// must not quietly take its default away.
		if provider != name {
			agents = append(agents, name)
		}
	case "off":
		if provider == name {
			provider = ""
		}
	default:
		return nil, jsonapi.BadRequest("role is default, offered or unused")
	}
	if a.db != nil {
		a.db.Set(ImageTable, "provider", provider)
		a.db.Set(ImageTable, "agents", agents)
	}
	acceptChanges(name)
	adminChange(r, activity.Settings, "image backend %s: %s (default=%q, offered: %s)", name, role, provider, strings.Join(agents, ", "))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// imageBackendListed is a connector the picker offers: every image
// connector, and any other backend already registered (a peer's renderer).
func imageBackendListed(conn Connector) bool {
	return imageConnector(conn.Name) || (conn.Approved && ImageBackendRegistered(conn.Name))
}

// imageConnector reports whether name is a connector that makes or edits
// pictures: a service giving image.generate or image.edit.
func imageConnector(name string) bool {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return false
	}
	return c.Kind == service.Kind && (service.Provides(c, "image.generate") || service.Provides(c, "image.edit"))
}

// handleTranscribeTest serves /api/transcribe/test; registerMediaRoutes says what it is for.
func (a *AdminApp) handleTranscribeTest(c *jsonapi.Call, form withSettings[TranscribeConfig]) (any, error) {
	turnedOff(&form.Form.Provider, &form.Form.Enabled, "")
	req, conn := form.Form, chosenConnection(form.Form.Provider)
	req.APIKey = ""
	if !req.Enabled {
		return testResultBody(false, "", "transcription is disabled: choose what transcribes first"), nil
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
	return testResultBody(true, "OK: "+ProviderTitle(req.Provider)+" transcribed a second of a tone and heard "+heard, ""), nil
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
	if name, ok := media.ChosenConnection(strings.TrimSpace(resolved.Provider)); ok {
		c, _ := GetConnector(RootDB, name)
		resolved.Model = service.DefaultWith(c, "embed.text", "model", values)
	}
	return resolved, nil
}

// serviceProviderOK refuses a provider that names a service connector which
// is missing or does not provide capability. Anything else (this instance, a
// peer) is left to its own resolution. Naming one here references it, and
// the page's save accepts a change waiting on it (acceptChanges).
func serviceProviderOK(provider, capability string) error {
	name, ok := media.ChosenConnection(strings.TrimSpace(provider))
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
// it here is what references it. One with a change waiting says so: saving
// the page with it chosen accepts the change.
func serviceProviderOptions(capability, verb string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != service.Kind || !service.Provides(c, capability) {
			continue
		}
		label, help := connectorLabel(c), verb+" with "+c.Title()+": "+ConnectorSummary(c)+"."
		if ch, ok := service.PendingChange(RootDB, c.Name); ok {
			label += " (changed, waiting to be accepted)"
			help += " " + changedNote(ch)
		}
		out = append(out, ui.SelectOption{Value: c.Name, Label: label, Help: help})
	}
	return out
}

// changedNote says who changed a connector and how to accept it.
func changedNote(ch service.ConnectorChange) string {
	by := ch.By
	if by == "" {
		by = "someone who is not an administrator"
	}
	return "Changed by " + by + " on " + ch.At.Format("2006-01-02 15:04") + ", so no call reaches it: read its spec under Connectors, then save this page with it chosen to accept the change."
}

// disabledOption is "Disabled", first in a page's list for a job that can
// be off (embedding, transcription, speech, pictures, web search).
var disabledOption = ui.SelectOption{Value: "none", Label: "Disabled"}

// turnedOff reads a page's choice back: "none" (Disabled) turns the job off
// and keeps what was chosen before (stored), so turning it on again starts
// from it; any other choice turns it on. A post naming no choice leaves
// enabled as it came.
func turnedOff(provider *string, enabled *bool, stored string) {
	switch strings.TrimSpace(*provider) {
	case "none":
		*provider, *enabled = stored, false
	case "":
	default:
		*enabled = true
	}
}

// shownOff is a page's values with its choice shown as Disabled while the
// job is off: the page has no separate switch.
func shownOff(out map[string]any, enabled bool) map[string]any {
	if !enabled {
		out["provider"] = "none"
	}
	return out
}

// acceptChanges accepts the changes waiting on connectors a page names,
// given as its settings' values: the administrator saving the page accepts
// them as they are.
func acceptChanges(named ...string) {
	var names []string
	for _, n := range named {
		if name, ok := media.ChosenConnection(n); ok {
			names = append(names, name)
		}
	}
	service.AcceptChanges(RootDB, names...)
}

// previewTieIn lets the settings page's own test reach the connector its
// form names (a provider setting) while a change holds it: under the
// returned context; end ends it.
func previewTieIn(ctx context.Context, provider string) (context.Context, func()) {
	name, ok := media.ChosenConnection(strings.TrimSpace(provider))
	if !ok {
		return ctx, func() {}
	}
	return service.Preview(ctx, name)
}

// connectorLabel is how a page's choices show connector c: the name it is
// shown as (Title).
func connectorLabel(c Connector) string { return c.Title() }
