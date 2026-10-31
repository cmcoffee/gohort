package service

// Image Generation's Gemini and OpenAI providers were written in Go, before
// every capability came from a connector: a provider name and a key in the
// settings, or no key and the matching LLM's. This moves a setting that named
// one onto a service connector once, at startup, so there is one way a
// picture is made: the connector is the template gohort ships for that
// provider, holding the key the settings used, and can be read, tested, fixed
// and replaced. A setting with no key anywhere never made a picture: it is
// turned off rather than moved.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/recipes"
)

// MigrateImages moves Image Generation's built-in Gemini or OpenAI provider
// onto a service connector, once (Admin > Migrations shows it ran). It needs
// the credential store, for the key.
func MigrateImages(db Database) {
	NewMigrationRunner("core", "").Once("images_to_connector:v1", func() int {
		changed, err := migrateImages(db)
		if err != nil {
			// Recorded on the marker, where Admin > Migrations shows it.
			panic(fmt.Sprintf("image generation was not moved to a connector: %v", err))
		}
		return changed
	})
}

// migrateImages is MigrateImages' work: a setting naming gemini or openai (a
// blank one meant gemini) with a key becomes that provider's template,
// installed as an approved connector with the key in its credential, and the
// setting names the connector. It reports how many records it changed.
func migrateImages(db Database) (int, error) {
	if db == nil {
		return 0, nil
	}
	var stored, key string
	db.Get(ImageTable, "provider", &stored)
	db.Get(ImageTable, "api_key", &key)
	provider := strings.TrimSpace(stored)
	if provider == "" {
		provider = "gemini" // what a blank setting used
	}
	if provider != "gemini" && provider != "openai" {
		return 0, nil // off, or a connector already
	}
	if key = strings.TrimSpace(key); key == "" {
		key = llmKeyFor(db, provider)
	}
	if key == "" {
		// No key anywhere: no picture was ever made. Off says so, where the
		// old name would now name nothing.
		if stored == "" {
			return 0, nil
		}
		db.Set(ImageTable, "provider", "")
		Log("[migration] image generation named %s with no key, so it made no pictures: it is off until a connector is chosen", provider)
		return 1, nil
	}
	cred, conn, err := imageTemplate(db, provider+"-images", key)
	if err != nil {
		return 0, err
	}
	changed := 0
	cred.Name, cred.Disabled = freeCredentialName(cred.Name), false
	if err := Secure().Save(cred, key); err != nil {
		return changed, fmt.Errorf("saving the key as credential %s: %w", cred.Name, err)
	}
	changed++
	var spec map[string]any
	if err := json.Unmarshal(conn.Spec, &spec); err != nil {
		return changed, err
	}
	if p, ok := spec["provider"].(map[string]any); ok {
		p["auth"] = map[string]any{"credential": cred.Name}
	}
	if conn.Spec, err = json.Marshal(spec); err != nil {
		return changed, err
	}
	conn.Name, conn.Kind = freeConnectorName(db, conn.Name), Kind
	conn.Desc += ", moved here from the Image Generation settings"
	if err := SaveConnector(db, conn); err != nil {
		return changed, fmt.Errorf("saving connector %s: %w", conn.Name, err)
	}
	if err := ApproveConnector(db, conn.Name); err != nil {
		return changed, fmt.Errorf("approving connector %s: %w", conn.Name, err)
	}
	changed++
	db.Set(ImageTable, "provider", conn.Name)
	db.Unset(ImageTable, "api_key")
	changed++
	Log("[migration] image generation now comes from connector %s (the %s template, with the key the settings used)", conn.Name, provider)
	return changed, nil
}

// llmKeyFor is the key an LLM tier on provider holds, the worker's first:
// what Image Generation used when it was given none of its own.
func llmKeyFor(db Database, provider string) string {
	for _, table := range []string{LLMTable, LeadLLMTable} {
		var p, k string
		db.Get(table, "provider", &p)
		db.Get(table, "api_key", &k)
		if p == provider && strings.TrimSpace(k) != "" {
			return strings.TrimSpace(k)
		}
	}
	return ""
}

// imageTemplate is a built-in image template filled in with its defaults:
// its credential and its connector, as Add would install them.
func imageTemplate(db Database, id, key string) (SecureCredential, Connector, error) {
	var cred SecureCredential
	var conn Connector
	r, _, ok := recipes.Get(db, id)
	if !ok {
		return cred, conn, fmt.Errorf("the template %s is not on this gohort", id)
	}
	bundle, _, _, err := recipes.Fill(r, map[string]string{"api_key": key})
	if err != nil {
		return cred, conn, err
	}
	for _, a := range bundle.Artifacts {
		switch a.Type {
		case "credential":
			err = json.Unmarshal(a.Recipe, &cred)
		case "connector":
			err = json.Unmarshal(a.Recipe, &conn)
		}
		if err != nil {
			return cred, conn, fmt.Errorf("the template %s: %w", id, err)
		}
	}
	if cred.Name == "" || conn.Name == "" {
		return cred, conn, fmt.Errorf("the template %s has no credential or connector", id)
	}
	return cred, conn, nil
}
