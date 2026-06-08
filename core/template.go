package core

import (
	"encoding/json"
	"fmt"
)

// Template bundle format: the on-disk shape of a pre-written integration
// (or any artifact recipe) that ships in the repo under extras/templates/
// and can be picked and imported into a deployment. The goal is to close
// the "starter integration catalog" gap without abandoning the
// generalize-don't-specialize ethos. A template is pre-authored Builder
// output as DATA: forkable after import, not a baked-in feature. Git gives
// versioning, review, and a contribution path (PRs) for free, so there is
// no marketplace backend to run.
//
// SECRETS ARE NEVER IN A TEMPLATE. A SecureCredential carries only config
// shape; its secret lives under a separate DB key (see secure_api.go), so a
// marshalled credential is already secret-free. The manifest's
// RequiresSecrets declares what the importer must collect from the user;
// imported credentials stay Pending until the secret is filled in and the
// admin runs Test.

// TemplateSchemaVersion is the highest bundle schema this build understands.
// Bump it on any breaking change to the on-disk shape; ParseTemplate refuses
// a bundle authored against a newer schema so an old binary never silently
// mis-imports a future template.
const TemplateSchemaVersion = 1

// TemplateManifest is the human- and gallery-facing header of a template.
type TemplateManifest struct {
	Name            string           `json:"name"`               // stable id, kebab-case ("github")
	Title           string           `json:"title"`              // display name ("GitHub")
	Description     string           `json:"description"`        // one-line gallery blurb
	Category        string           `json:"category,omitempty"` // "dev", "comms", "productivity", ...
	SchemaVersion   int              `json:"schema_version"`
	RequiresSecrets []TemplateSecret `json:"requires_secrets,omitempty"`
	SetupNotes      string           `json:"setup_notes,omitempty"` // shown during import
}

// TemplateSecret declares one secret the importer must collect from the
// user. Credential names the SecureCredential (by .Name) the secret belongs
// to, so the import flow can land the value under the right credential.
type TemplateSecret struct {
	Credential string `json:"credential"`     // SecureCredential.Name this secret fills
	Label      string `json:"label"`          // "GitHub Personal Access Token"
	Help       string `json:"help,omitempty"` // where/how to obtain it
}

// TemplateBundle is the full serialized recipe. Credentials, Tools, and
// Skills are core types and travel concretely. App-specific artifacts
// (orchestrate agents, pipelines, ...) ride in Extras as opaque JSON keyed
// by kind, so core stays domain-agnostic: each app registers an importer
// for its own kind rather than core knowing about AgentRecord.
type TemplateBundle struct {
	Manifest    TemplateManifest             `json:"manifest"`
	Credentials []SecureCredential           `json:"credentials,omitempty"`
	Tools       []TempTool                   `json:"tools,omitempty"`
	Skills      []SkillRecord                `json:"skills,omitempty"`
	Extras      map[string][]json.RawMessage `json:"extras,omitempty"` // kind -> []artifact JSON
}

// MarshalTemplate serializes a bundle to indented JSON suitable for checking
// into the repo. It stamps the current schema version when unset. This is
// the single chokepoint to enforce the no-secret invariant if the artifact
// structs ever gain a sensitive field (none today).
func MarshalTemplate(b TemplateBundle) ([]byte, error) {
	if b.Manifest.SchemaVersion == 0 {
		b.Manifest.SchemaVersion = TemplateSchemaVersion
	}
	return json.MarshalIndent(b, "", "  ")
}

// ParseTemplate deserializes and validates a bundle: the schema version is
// understood, the manifest names itself, and every declared secret points at
// a credential the bundle actually ships (otherwise the import flow would
// prompt for a secret with nowhere to put it).
func ParseTemplate(data []byte) (TemplateBundle, error) {
	var b TemplateBundle
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("template parse: %w", err)
	}
	if b.Manifest.Name == "" {
		return b, fmt.Errorf("template parse: manifest.name is required")
	}
	if b.Manifest.SchemaVersion > TemplateSchemaVersion {
		return b, fmt.Errorf("template %q needs schema version %d but this build understands up to %d; upgrade gohort", b.Manifest.Name, b.Manifest.SchemaVersion, TemplateSchemaVersion)
	}
	have := make(map[string]bool, len(b.Credentials))
	for _, c := range b.Credentials {
		have[c.Name] = true
	}
	for _, s := range b.Manifest.RequiresSecrets {
		if !have[s.Credential] {
			return b, fmt.Errorf("template %q declares a secret for credential %q that the bundle does not define", b.Manifest.Name, s.Credential)
		}
	}
	return b, nil
}
