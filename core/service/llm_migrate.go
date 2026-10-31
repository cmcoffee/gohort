package service

// An LLM tier on Anthropic, OpenAI or Gemini kept its key in the LLM
// settings, beside the provider's name. This moves such a tier onto a model
// connector once, at startup, so the key lives in a credential and the
// address in a spec that can be read, tested and fixed: the tier names the
// connector, and keeps its model, context and thinking settings, which are
// the tier's and not the service's. The model clients stay Go: a connector
// names the dialect gohort's client speaks to it.
//
// What stays: Bedrock signs its requests with the machine's AWS credentials,
// and the local servers (Ollama, llama.cpp, vLLM) carry thinking switches and
// a queue that are Go; a peer moves with the peers.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// modelTemplates are the templates a tier's hosted provider moves to.
var modelTemplates = map[string]string{"anthropic": "anthropic-model", "openai": "openai-model", "gemini": "gemini-model"}

// modelDefaults are the models each client ran when a tier named none: the
// connector's default, so a tier that names none runs what it did.
var modelDefaults = map[string]string{"anthropic": "claude-sonnet-5", "openai": "gpt-4o", "gemini": "gemini-2.5-flash"}

// ModelTemplate is the template a tier on provider runs through, as a model
// connector made from it: anthropic, openai and gemini have one; anything
// else gives "".
func ModelTemplate(provider string) string {
	return modelTemplates[strings.ToLower(strings.TrimSpace(provider))]
}

// ModelDefault is the model gohort's client for provider runs when a tier
// names none, or "".
func ModelDefault(provider string) string {
	return modelDefaults[strings.ToLower(strings.TrimSpace(provider))]
}

// ModelTemplateOf is the template a model connector named name was made
// from, or "".
func ModelTemplateOf(db Database, name string) string {
	c, ok := GetConnector(db, name)
	if !ok || c.Kind != Kind {
		return ""
	}
	return c.Template
}

// ModelConnectorFor gives the approved model connector a tier on provider
// (anthropic, openai or gemini) runs on with key: one made from that
// provider's template already holding the same key, or else the template
// installed with it. An OpenAI tier sent to an address of its own (endpoint)
// takes the OpenAI-compatible template at that address, which is where its
// requests went. model is the connector's default, for a tier that names
// none. from names the settings, for the connector's description. It returns
// the connector's name and how many records it wrote.
func ModelConnectorFor(db Database, provider, endpoint, model, key, from string) (string, int, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	id, ok := modelTemplates[provider]
	if !ok {
		return "", 0, fmt.Errorf("%s is not a hosted model provider", provider)
	}
	if key = strings.TrimSpace(key); key == "" {
		return "", 0, fmt.Errorf("%s needs its API key", provider)
	}
	if model = strings.TrimSpace(model); model == "" {
		model = ModelDefault(provider)
	}
	answers := map[string]string{"api_key": key, "model": model}
	if ep := strings.TrimRight(strings.TrimSpace(endpoint), "/"); provider == "openai" && ep != "" && ep != "https://api.openai.com/v1" {
		id, answers["base_url"] = "openai-compatible", ep
	}
	if name, ok := sameKeyConnector(db, id, answers["base_url"], key); ok {
		return name, 0, nil
	}
	return installMoved(db, id, answers, key, from)
}

// RekeyModelConnector puts key into the credential of the model connector
// named name, a tier's own on provider: how a key typed on the tier's form
// replaces the one it ran with, rather than leaving that connector behind and
// making another. It does nothing, and says so, when name was not made from
// provider's template, or when a connector already holds key (that one is the
// tier's to run on). The caller keeps a connector both tiers run on out of
// it: the other tier's key would change unseen.
func RekeyModelConnector(db Database, provider, name, key string) (bool, error) {
	id := ModelTemplate(provider)
	if key = strings.TrimSpace(key); id == "" || key == "" || ModelTemplateOf(db, name) != id {
		return false, nil
	}
	if _, held := sameKeyConnector(db, id, "", key); held {
		return false, nil
	}
	c, _ := GetConnector(db, name)
	s, err := specOf(c)
	if err != nil {
		return false, err
	}
	credName := strings.TrimSpace(s.Provider.Auth.Credential)
	cred, ok := Secure().Load(credName)
	if credName == "" || !ok {
		return false, nil
	}
	if err := Secure().Save(cred, key); err != nil {
		return false, fmt.Errorf("saving the new key in credential %s: %w", credName, err)
	}
	return true, nil
}

// sameKeyConnector is an approved connector made from template id (at
// base, when given) whose credential holds key.
func sameKeyConnector(db Database, id, base, key string) (string, bool) {
	for _, c := range ListConnectors(db) {
		if c.Kind != Kind || c.Template != id || !c.Approved {
			continue
		}
		s, err := specOf(c)
		if err != nil || s.Provider.Auth.Credential == "" {
			continue
		}
		addr := strings.TrimRight(strings.TrimSpace(s.Provider.BaseURL), "/")
		if base != "" && addr != base {
			continue
		}
		if k, err := Secure().ModelKey(s.Provider.Auth.Credential, addr+"/"); err == nil && k == key {
			return c.Name, true
		}
	}
	return "", false
}

// MigrateLLMTiers moves the worker and lead tiers on Anthropic, OpenAI or
// Gemini onto model connectors, once (Admin > Migrations shows it ran). It
// needs the credential store, for the keys.
func MigrateLLMTiers(db Database) {
	NewMigrationRunner("core", "").Once("llm_tiers_to_connectors:v1", func() int {
		changed, err := migrateLLMTiers(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("an LLM tier was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateLLMTiers is MigrateLLMTiers' work: each tier naming a hosted
// provider, with a key, names the model connector holding that key (both
// tiers on one key share one), and its key and address rows are gone. A tier
// with no key never ran, and is left as it is: the form asks for one. It
// reports how many records it changed.
func migrateLLMTiers(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	total := 0
	for _, t := range []struct{ table, title string }{{LLMTable, "Worker LLM"}, {LeadLLMTable, "Lead LLM"}} {
		var provider, model, key, endpoint string
		db.Get(t.table, "provider", &provider)
		db.Get(t.table, "model", &model)
		db.Get(t.table, "api_key", &key)
		db.Get(t.table, "endpoint", &endpoint)
		provider = strings.ToLower(strings.TrimSpace(provider))
		if ModelTemplate(provider) == "" {
			continue // a local server, Bedrock, a peer, a connector already, or not set
		}
		if strings.TrimSpace(key) == "" {
			Log("[migration] the %s names %s with no key, so it made no calls: it is left as it is until a key or a connector is chosen", t.title, provider)
			continue
		}
		name, changed, err := ModelConnectorFor(db, provider, endpoint, model, key, t.title)
		total += changed
		if err != nil {
			return total, fmt.Errorf("the %s: %w", t.title, err)
		}
		db.Set(t.table, "provider", media.ServiceProviderPrefix+name)
		// A tier that named no model ran its client's default: it names it
		// now, since a connector both tiers share has the other's.
		if strings.TrimSpace(model) == "" {
			db.Set(t.table, "model", ModelDefault(provider))
		}
		db.Unset(t.table, "api_key")
		db.Unset(t.table, "endpoint")
		total++
		Log("[migration] the %s now runs on model connector %s (%s, as the settings had it)", t.title, name, provider)
	}
	return total, nil
}
