package service

// Embeddings were made by this instance itself, from an endpoint, model and
// key in the settings, before every capability came from a connector. This
// moves those settings onto a service connector once, at startup, so there is
// one way embeddings are made: the connector is what was in the settings,
// written as a spec that can be read, tested, fixed and replaced. The space
// the stored vectors are in does not change (the settings keep their endpoint
// and model, which name it), so nothing is re-embedded. A fresh install gets
// the same connector from the defaults, with embeddings off.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// embedMigrationName is the connector the settings become, or the first
// free name after it.
const embedMigrationName = "embeddings"

// The defaults a fresh install's connector starts from: Ollama on this
// machine.
const (
	embedDefaultEndpoint = "http://localhost:11434/api"
	embedDefaultModel    = "nomic-embed-text"
)

// MigrateEmbeddings moves embeddings this instance made itself onto a
// service connector, once (Admin > Migrations shows it ran). It needs the
// credential store, for a key the settings held.
func MigrateEmbeddings(db Database) {
	NewMigrationRunner("core", "").Once("embeddings_to_connector:v1", func() int {
		changed, err := migrateEmbeddings(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("embeddings were not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateEmbeddings is MigrateEmbeddings' work: the settings, when they name
// no connector or peer, become the approved connector embedMigrationName
// (and any key the credential embeddings_key), and the settings name it.
// It reports how many records it changed.
func migrateEmbeddings(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	var cfg EmbeddingConfig
	stored := db.Get(EmbeddingTable, "current", &cfg)
	if p := strings.TrimSpace(cfg.Provider); p != "" && p != EmbeddingProviderLocal {
		return 0, nil // a connector or a peer already
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		// Never set up: the defaults, so turning embeddings on is all it
		// takes on a machine running Ollama.
		cfg.Endpoint = embedDefaultEndpoint
		if strings.TrimSpace(cfg.Model) == "" {
			cfg.Model = embedDefaultModel
		}
	}
	changed := 0
	cred := ""
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		cred = freeCredentialName("embeddings_key")
		c := SecureCredential{Name: cred, Type: SecureCredBearer, BaseURL: strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"),
			Description: "The key embeddings were sent with, moved here from the embeddings settings."}
		if err := Secure().Save(c, key); err != nil {
			return changed, fmt.Errorf("saving the key as credential %s: %w", cred, err)
		}
		changed++
	}
	spec, err := json.Marshal(embedMigrationSpec(cfg, cred))
	if err != nil {
		return changed, err
	}
	name := freeConnectorName(db, embedMigrationName)
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "the server's own model"
	}
	c := Connector{Name: name, Kind: Kind, Spec: spec,
		Desc: "Embeddings at " + cfg.Endpoint + " (" + model + "), moved here from the embeddings settings."}
	if err := SaveConnector(db, c); err != nil {
		return changed, fmt.Errorf("saving connector %s: %w", name, err)
	}
	if err := ApproveConnector(db, name); err != nil {
		return changed, fmt.Errorf("approving connector %s: %w", name, err)
	}
	changed++
	// The endpoint and model stay as they were: they name the space the
	// stored vectors are in (EmbedVersion), and the model is the one each
	// embed asks the connector for. The key now lives in the credential.
	cfg.Provider, cfg.APIKey = media.ServiceProviderPrefix+name, ""
	db.Set(EmbeddingTable, "current", cfg)
	SetEmbeddingConfig(cfg)
	if stored {
		changed++
	}
	Log("[migration] embeddings now come from connector %s (%s), as the embeddings settings had them", name, cfg.Endpoint)
	return changed, nil
}

// embedMigrationSpec is the connector spec for embeddings as cfg made them:
// a POST of the texts to {endpoint}/embeddings, the model named when there
// is one (a single-model server is not sent one it would refuse), and the
// vectors read from whichever of the three shapes the server answers in
// (OpenAI's data[].embedding, Ollama's embeddings, or one embedding).
func embedMigrationSpec(cfg EmbeddingConfig, credential string) map[string]any {
	body := map[string]any{"input": "{texts}"}
	if strings.TrimSpace(cfg.Model) != "" {
		body["model"] = "{model}"
	}
	auth := map[string]any{"none": true}
	if credential != "" {
		auth = map[string]any{"credential": credential}
	}
	binding := map[string]any{
		"capability": "embed.text",
		"steps":      []any{map[string]any{"call": "embed"}},
		"output": map[string]any{"vectors": map[string]any{"path": "data", "each": "embedding", "as": "json",
			"or": []any{
				map[string]any{"path": "embeddings", "as": "json"},
				map[string]any{"path": "embedding", "as": "json", "single": true},
			}}},
	}
	if m := strings.TrimSpace(cfg.Model); m != "" {
		binding["defaults"] = map[string]any{"model": m}
	}
	return map[string]any{
		"provider": map[string]any{
			"transport":  "http",
			"base_url":   strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"),
			"auth":       auth,
			"operations": map[string]any{"embed": map[string]any{"method": "POST", "path": "/embeddings", "body": body}},
		},
		"provides": []any{binding},
	}
}

// freeConnectorName is name, or name-2, name-3 ... when it is taken.
func freeConnectorName(db Database, name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		if _, taken := GetConnector(db, n); !taken {
			return n
		}
	}
}

// freeCredentialName is name, or name_2, name_3 ... when it is taken.
func freeCredentialName(name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s_%d", name, i)
		}
		if _, taken := Secure().Resolve(n, ""); !taken {
			return n
		}
	}
}
