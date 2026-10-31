package service

// Transcription was done by this instance itself, from an endpoint, model and
// key in the settings, before every capability came from a connector. This
// moves those settings onto a service connector once, at startup, so there is
// one way audio is transcribed: the connector is what was in the settings,
// written as a spec that can be read, tested, fixed and replaced. Settings
// that never named an endpoint have nothing to move: transcription stays off
// until a connector or a peer is chosen.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// transcribeMigrationName is the connector the settings become, or the
// first free name after it.
const transcribeMigrationName = "transcription"

// transcribeMigrationTimeout is the old transcriber's five minutes, for a
// long recording; its size limit is audioMaxBytes.
const transcribeMigrationTimeout = 300

// MigrateTranscription moves transcription this instance did itself onto a
// service connector, once (Admin > Migrations shows it ran). It needs the
// credential store, for a key the settings held.
func MigrateTranscription(db Database) {
	NewMigrationRunner("core", "").Once("transcription_to_connector:v1", func() int {
		changed, err := migrateTranscription(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("transcription was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateTranscription is MigrateTranscription's work: settings that name an
// endpoint and no connector or peer become the approved connector
// transcribeMigrationName (and any key the credential transcription_key),
// and the settings name it. It reports how many records it changed.
func migrateTranscription(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	var cfg TranscribeConfig
	if !db.Get(TranscribeTable, "current", &cfg) {
		return 0, nil // never set up
	}
	if p := strings.TrimSpace(cfg.Provider); p != "" && p != EmbeddingProviderLocal {
		return 0, nil // a connector or a peer already
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return 0, nil // nothing was set up to move
	}
	changed := 0
	cred := ""
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		cred = freeCredentialName("transcription_key")
		c := SecureCredential{Name: cred, Type: SecureCredBearer, BaseURL: endpoint,
			Description: "The key transcription was sent with, moved here from the transcription settings."}
		if err := Secure().Save(c, key); err != nil {
			return changed, fmt.Errorf("saving the key as credential %s: %w", cred, err)
		}
		changed++
	}
	spec, err := json.Marshal(transcribeMigrationSpec(cfg, cred))
	if err != nil {
		return changed, err
	}
	name := freeConnectorName(db, transcribeMigrationName)
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "the server's own model"
	}
	c := Connector{Name: name, Kind: Kind, Spec: spec,
		Desc: "Transcription at " + endpoint + " (" + model + "), moved here from the transcription settings."}
	if err := SaveConnector(db, c); err != nil {
		return changed, fmt.Errorf("saving connector %s: %w", name, err)
	}
	if err := ApproveConnector(db, name); err != nil {
		return changed, fmt.Errorf("approving connector %s: %w", name, err)
	}
	changed++
	// The model stays: it is what a peer borrowing transcription from here is
	// told it gets. The endpoint and key are the connector's now.
	cfg.Provider, cfg.Endpoint, cfg.APIKey = media.ServiceProviderPrefix+name, "", ""
	db.Set(TranscribeTable, "current", cfg)
	SetTranscribeConfig(cfg)
	changed++
	Log("[migration] transcription now comes from connector %s (%s), as the transcription settings had it", name, endpoint)
	return changed, nil
}

// transcribeMigrationSpec is the connector spec for transcription as cfg did
// it: the audio posted as a multipart form to {endpoint}/audio/transcriptions,
// the model named when there is one (whisper.cpp takes none), the words the
// speaker is likely to say as its prompt, and the text read from the JSON
// reply every OpenAI-shaped transcriber gives by default.
func transcribeMigrationSpec(cfg TranscribeConfig, credential string) map[string]any {
	body := map[string]any{"file": "{audio}", "prompt": "{prompt}", "language": "{language}", "response_format": "json"}
	if strings.TrimSpace(cfg.Model) != "" {
		body["model"] = "{model}"
	}
	auth := map[string]any{"none": true}
	if credential != "" {
		auth = map[string]any{"credential": credential}
	}
	binding := map[string]any{
		"capability": "speech.transcribe",
		"steps":      []any{map[string]any{"call": "transcribe"}},
		"output":     map[string]any{"text": map[string]any{"path": "text", "as": "text"}},
	}
	if m := strings.TrimSpace(cfg.Model); m != "" {
		binding["defaults"] = map[string]any{"model": m}
	}
	return map[string]any{
		"provider": map[string]any{
			"transport": "http",
			"base_url":  strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"),
			"auth":      auth,
			"limits":    map[string]any{"max_bytes": audioMaxBytes},
			"operations": map[string]any{"transcribe": map[string]any{"method": "POST", "path": "/audio/transcriptions",
				"send": "multipart", "body": body, "timeout_secs": transcribeMigrationTimeout}},
		},
		"provides": []any{binding},
	}
}
