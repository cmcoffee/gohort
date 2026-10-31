package service

// Speech was made by this instance itself, from an endpoint, model, voice and
// key in the settings, before every capability came from a connector. This
// moves those settings onto a service connector once, at startup, so there is
// one way text is spoken: the connector is what was in the settings, written
// as a spec that can be read, tested, fixed and replaced. Settings that never
// named an endpoint have nothing to move: speech stays off until a connector
// or a peer is chosen.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// speechMigrationName is the connector the settings become, or the first
// free name after it.
const speechMigrationName = "speech"

// speechMigrationMaxBytes is the most audio the old speaker took back.
const speechMigrationMaxBytes = 32 << 20

// MigrateSpeech moves speech this instance made itself onto a service
// connector, once (Admin > Migrations shows it ran). It needs the credential
// store, for a key the settings held.
func MigrateSpeech(db Database) {
	NewMigrationRunner("core", "").Once("speech_to_connector:v1", func() int {
		changed, err := migrateSpeech(db, speechHasVoices)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("speech was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateSpeech is MigrateSpeech's work: settings that name an endpoint and
// no connector or peer become the approved connector speechMigrationName
// (and any key the credential speech_key), and the settings name it.
// hasVoices says whether the server lists its voices. It reports how many
// records it changed.
func migrateSpeech(db Database, hasVoices func(endpoint, key string) bool) (int, error) {
	if db == nil {
		return 0, nil
	}
	var cfg media.SpeechConfig
	if !db.Get(media.SpeechTable, "current", &cfg) {
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
		cred = freeCredentialName("speech_key")
		c := SecureCredential{Name: cred, Type: SecureCredBearer, BaseURL: endpoint,
			Description: "The key speech was asked for with, moved here from the speech settings."}
		if err := Secure().Save(c, key); err != nil {
			return changed, fmt.Errorf("saving the key as credential %s: %w", cred, err)
		}
		changed++
	}
	spec, err := json.Marshal(speechMigrationSpec(cfg, cred, hasVoices(endpoint, cfg.APIKey)))
	if err != nil {
		return changed, err
	}
	name := freeConnectorName(db, speechMigrationName)
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "the server's own model"
	}
	c := Connector{Name: name, Kind: Kind, Spec: spec,
		Desc: "Speech at " + endpoint + " (" + model + "), moved here from the speech settings."}
	if err := SaveConnector(db, c); err != nil {
		return changed, fmt.Errorf("saving connector %s: %w", name, err)
	}
	if err := ApproveConnector(db, name); err != nil {
		return changed, fmt.Errorf("approving connector %s: %w", name, err)
	}
	changed++
	// The voice stays: it is chosen on the settings page, from the
	// connector's list. The model stays too: it is what a peer borrowing
	// speech from here is told. The endpoint and key are the connector's now.
	cfg.Provider, cfg.Endpoint, cfg.APIKey = media.ServiceProviderPrefix+name, "", ""
	db.Set(media.SpeechTable, "current", cfg)
	media.SetSpeechConfig(cfg)
	changed++
	Log("[migration] speech now comes from connector %s (%s), as the speech settings had it", name, endpoint)
	return changed, nil
}

// speechHasVoices asks the server for its voice list once, as the move is
// made: only a server that says it has none (404, 405, 501) is written
// without one. One that cannot be reached right now keeps it, and an
// OpenAI-shaped server with no list fails only that part of its Test.
func speechHasVoices(endpoint, key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/audio/voices", nil)
	if err != nil {
		return true
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return true
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false
	}
	return true
}

// speechMigrationSpec is the connector spec for speech as cfg made it: the
// text posted as JSON to {endpoint}/audio/speech for mp3, with the model and
// voice when there are ones (the settings' voice is the default; the voice
// an agent or the settings name is sent in its place), the audio read from
// the reply, and with voices the list GET {endpoint}/audio/voices gives
// (Kokoro-FastAPI's shape).
func speechMigrationSpec(cfg media.SpeechConfig, credential string, voices bool) map[string]any {
	body := map[string]any{"input": "{text}", "voice": "{voice}", "response_format": "mp3"}
	defaults := map[string]any{}
	if m := strings.TrimSpace(cfg.Model); m != "" {
		body["model"] = "{model}"
		defaults["model"] = m
	}
	if v := strings.TrimSpace(cfg.Voice); v != "" {
		defaults["voice"] = v
	}
	auth := map[string]any{"none": true}
	if credential != "" {
		auth = map[string]any{"credential": credential}
	}
	speak := map[string]any{
		"capability": "speech.synthesize",
		"steps":      []any{map[string]any{"call": "speak"}},
		"output":     map[string]any{"audio": map[string]any{"from": "body"}},
	}
	if len(defaults) > 0 {
		speak["defaults"] = defaults
	}
	ops := map[string]any{"speak": map[string]any{"method": "POST", "path": "/audio/speech", "body": body, "read": "file"}}
	provides := []any{speak}
	if voices {
		ops["voices"] = map[string]any{"path": "/audio/voices"}
		provides = append(provides, map[string]any{
			"capability": "speech.voices",
			"steps":      []any{map[string]any{"call": "voices"}},
			"output":     map[string]any{"voices": map[string]any{"path": "voices", "as": "json"}},
		})
	}
	return map[string]any{
		"provider": map[string]any{
			"transport":  "http",
			"base_url":   strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"),
			"auth":       auth,
			"limits":     map[string]any{"max_bytes": speechMigrationMaxBytes},
			"operations": ops,
		},
		"provides": provides,
	}
}
