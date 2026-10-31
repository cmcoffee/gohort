package admin

// gohort's own providers, read as connectors (docs/design/connectors-and-
// flows.md, stage 3). The local embedder, transcriber and speaker and the
// Gemini and OpenAI image code are wrappers written in Go rather than specs:
// the same inside (a contract), an outside they reach, a test. Listing them
// with the other connectors means every capability's choice is among
// connectors, built in or added.
//
// Nothing here is stored as a connector. Each row is read from the settings
// the provider already uses, is changed there, and cannot be approved,
// exported or deleted: it is part of gohort.

import (
	"fmt"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
)

// builtinPrefix names a built-in row. A connector's name cannot hold a
// colon, so one can never be taken for the other.
const builtinPrefix = "built-in:"

// builtinConnector is one built-in provider as a connector row.
type builtinConnector struct {
	name    string
	wrapper string // inside ⇐ mapping ⇐ outside, as a service's line reads
	status  string // in use or not, and where it is set up
	// test runs the provider's own settings test on what is stored; nil
	// when there is nothing of its own to test (another provider is set).
	test func(c *jsonapi.Call) (any, error)
}

// builtinConnectors are gohort's own providers, read from their settings.
func (a *AdminApp) builtinConnectors() []builtinConnector {
	var out []builtinConnector

	emb := a.storedEmbeddingConfig()
	b := builtinConnector{name: builtinPrefix + "embeddings",
		wrapper: "Embeddings ⇐ " + withModel("embeddings", emb.Model) + " ⇐ " + endpointHost(emb.Endpoint, emb.APIKey)}
	switch {
	case !isLocal(emb.Provider):
		b.status = "Not in use: Embeddings are made by " + providerName(emb.Provider) + ". Set up under Admin > Capabilities > Embeddings."
		b.wrapper = "Embeddings ⇐ embeddings ⇐ an endpoint of your choosing"
	case !emb.Enabled:
		b.status = "Off. Turn it on under Admin > Capabilities > Embeddings."
	default:
		b.status = "In use for Embeddings. Set up under Admin > Capabilities > Embeddings."
		b.test = func(c *jsonapi.Call) (any, error) { return a.handleEmbeddingsTest(c, emb) }
	}
	out = append(out, b)

	tr := a.storedTranscribeConfig()
	b = builtinConnector{name: builtinPrefix + "transcription",
		wrapper: "Transcription ⇐ " + withModel("audio/transcriptions", tr.Model) + " ⇐ " + endpointHost(tr.Endpoint, tr.APIKey)}
	switch {
	case !isLocal(tr.Provider):
		b.status = "Not in use: transcription is done by " + providerName(tr.Provider) + ". Set up under Admin > Capabilities > Audio Transcription."
		b.wrapper = "Transcription ⇐ audio/transcriptions ⇐ an endpoint of your choosing"
	case !tr.Enabled:
		b.status = "Off. Turn it on under Admin > Capabilities > Audio Transcription."
	default:
		b.status = "In use for transcription. Set up under Admin > Capabilities > Audio Transcription."
		b.test = func(c *jsonapi.Call) (any, error) { return a.handleTranscribeTest(c, tr) }
	}
	out = append(out, b)

	sp := a.storedSpeechConfig()
	b = builtinConnector{name: builtinPrefix + "speech",
		wrapper: "Speech ⇐ " + withModel("audio/speech", sp.Model) + " ⇐ " + endpointHost(sp.Endpoint, sp.APIKey)}
	switch {
	case !isLocal(sp.Provider):
		b.status = "Not in use: speech is made by " + providerName(sp.Provider) + ". Set up under Admin > Capabilities > Speech (TTS)."
		b.wrapper = "Speech ⇐ audio/speech ⇐ an endpoint of your choosing"
	case !sp.Enabled:
		b.status = "Off. Turn it on under Admin > Capabilities > Speech (TTS)."
	default:
		b.status = "In use for speech. Set up under Admin > Capabilities > Speech (TTS)."
		b.test = func(c *jsonapi.Call) (any, error) { return a.handleSpeechTest(c, sp) }
	}
	out = append(out, b)

	chosen := a.storedString(ImageTable, "provider")
	if chosen == "" {
		chosen = "gemini"
	}
	for _, img := range []struct{ id, label, host string }{
		{"gemini", "Gemini", "generativelanguage.googleapis.com"},
		{"openai", "OpenAI", "api.openai.com"},
	} {
		id := img.id
		b = builtinConnector{name: builtinPrefix + id + "-images",
			wrapper: "Image Generation ⇐ " + img.label + " image model ⇐ " + img.host + " (key: Image Generation's, or the " + img.label + " LLM's)"}
		if chosen == id {
			b.status = "In use for Image Generation. Set up under Admin > Capabilities > Image Generation."
		} else {
			b.status = "Not in use: Image Generation is set to " + imageChoiceName(chosen) + ". Choose it under Admin > Capabilities > Image Generation."
		}
		b.test = func(c *jsonapi.Call) (any, error) {
			res, err := a.handleImageGenTest(c, imageGenConfig{Provider: id})
			// Its words are for the Image Generation form ("set one here").
			if body, ok := res.(map[string]any); ok {
				if why, _ := body["error"].(string); why != "" {
					body["error"] = strings.Replace(why, "set one here", "set one under Admin > Capabilities > Image Generation", 1)
				}
			}
			return res, err
		}
		out = append(out, b)
	}
	return out
}

// builtinTest runs the ?name= built-in's test and says how it went in the
// Connectors table's terms.
func (a *AdminApp) builtinTest(c *jsonapi.Call, name string) (any, error) {
	for _, b := range a.builtinConnectors() {
		if b.name != name {
			continue
		}
		if b.test == nil {
			return map[string]any{"message": b.status, "warnings": []string{b.status}}, nil
		}
		res, err := b.test(c)
		if err != nil {
			return nil, err
		}
		body, _ := res.(map[string]any)
		if ok, _ := body["ok"].(bool); ok {
			msg, _ := body["message"].(string)
			return map[string]any{"message": msg}, nil
		}
		why, _ := body["error"].(string)
		return map[string]any{"message": why, "warnings": []string{why}}, nil
	}
	return nil, jsonapi.NotFound("no built-in connector named " + name)
}

// isLocal reports whether a capability's provider is this instance.
func isLocal(provider string) bool {
	p := strings.TrimSpace(provider)
	return p == "" || p == EmbeddingProviderLocal
}

// providerName says which peer or service a capability uses, for a status.
func providerName(provider string) string {
	if name, ok := strings.CutPrefix(provider, media.ServiceProviderPrefix); ok {
		return "the connector " + name
	}
	if p, ok := PeerFromProvider(provider); ok {
		return "the peer " + p.Name
	}
	return provider
}

// imageChoiceName says what Image Generation is set to.
func imageChoiceName(chosen string) string {
	switch chosen {
	case "none":
		return "Disabled"
	case "gemini":
		return "the built-in Gemini"
	case "openai":
		return "the built-in OpenAI"
	}
	return "the connector " + chosen
}

// withModel is an endpoint path with the model it asks for, when one is set.
func withModel(path, model string) string {
	if m := strings.TrimSpace(model); m != "" {
		return fmt.Sprintf("%s (model %s)", path, m)
	}
	return path
}

// endpointHost is where a built-in reaches and with what, as a service's line
// says it: the host, and whether a key is set (never the key).
func endpointHost(endpoint, key string) string {
	host := strings.TrimSpace(endpoint)
	if host == "" {
		return "no endpoint set"
	}
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	if strings.TrimSpace(key) != "" {
		return host + " (key set)"
	}
	return host + " (no key)"
}
