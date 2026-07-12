// Artifact-type registrations: the concrete ArtifactType implementations that
// make each store portable through the unified bundle (core/artifact_pack.go).
// Each wraps that type's EXISTING export/import logic — the registry adds
// portability without the underlying stores knowing bundles exist. Adding a new
// portable type (agent, credential, pipeline) is one more implementation and one
// more RegisterArtifactType line here; the envelope, endpoints, and UI don't
// change.
package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func init() {
	RegisterArtifactType(connectorArtifact{})
	RegisterArtifactType(toolArtifact{})
	RegisterArtifactType(credentialArtifact{})
}

// ---- connector -------------------------------------------------------------

// connectorArtifact makes a Connector portable. Recipe is a PortableConnector
// (already identity-free / secret-free). Import goes through SaveConnector, so
// governance re-applies exactly as on create: remote_mcp / desktop_* land
// UNAPPROVED, rest_poll auto-approves (it reaches out only through an
// already-governed credential). Global (admin) scope — Owner is ignored.
type connectorArtifact struct{}

func (connectorArtifact) ArtifactType() string { return "connector" }

func (connectorArtifact) ListArtifacts(db Database) []ArtifactSel {
	var out []ArtifactSel
	for _, c := range ListConnectors(db) {
		out = append(out, ArtifactSel{Type: "connector", Name: c.Name})
	}
	return out
}

func (connectorArtifact) ExportArtifact(db Database, name, _ string) (json.RawMessage, error) {
	c, ok := GetConnector(db, name)
	if !ok {
		return nil, fmt.Errorf("no connector named %q", name)
	}
	return json.Marshal(toPortable(c))
}

func (connectorArtifact) ImportArtifact(db Database, recipe json.RawMessage, owner string) (string, string, error) {
	var pc PortableConnector
	if err := json.Unmarshal(recipe, &pc); err != nil {
		return "", "", fmt.Errorf("invalid connector recipe: %w", err)
	}
	name := strings.TrimSpace(pc.Name)
	if name == "" {
		return "", "", Error("missing connector name")
	}
	if _, exists := GetConnector(db, name); exists {
		return name, "a connector with this name already exists", nil
	}
	c := Connector{
		Name:  name,
		Kind:  strings.TrimSpace(pc.Kind),
		Desc:  pc.Desc,
		Spec:  pc.Spec,
		Owner: owner,
	}
	if err := SaveConnector(db, c); err != nil {
		return name, "", err
	}
	return name, "", nil
}

// ---- tool ------------------------------------------------------------------

// toolArtifact makes a persistent (or pending) TempTool portable. Recipe is the
// TempTool definition itself — the per-user governance/state wrapper
// (ApprovedAt / Shared / LastUsedAt) is identity and does NOT travel. The tool's
// Credential is a NAME reference, so no secret leaves the install.
//
// Per-user scope: export needs the owning user; import lands the tool in that
// user's PENDING pool via QueuePendingTempTool — never active. A tool recipe
// carries executable code (command_template / script_body / recipe files), so
// human approval before it can fire is the whole point. QueuePendingTempTool
// refuses to shadow an already-active tool of the same name (surfaced as a skip)
// and replaces a same-named pending draft in place.
type toolArtifact struct{}

func (toolArtifact) ArtifactType() string { return "tool" }

func (toolArtifact) ListArtifacts(db Database) []ArtifactSel {
	store := tempToolStore(db)
	if store == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range store.Keys(persistentTempToolsTable) {
		for _, p := range LoadPersistentTempTools(db, u) {
			out = append(out, ArtifactSel{Type: "tool", Name: p.Tool.Name, Owner: u})
		}
	}
	return out
}

func (toolArtifact) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("tool export requires an owner")
	}
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == name {
			return json.Marshal(p.Tool)
		}
	}
	// Fall back to the pending pool so a not-yet-approved draft can still be
	// shared for review.
	for _, p := range LoadPendingTempTools(db, owner) {
		if p.Tool.Name == name {
			return json.Marshal(p.Tool)
		}
	}
	return nil, fmt.Errorf("no tool named %q for user %q", name, owner)
}

func (toolArtifact) ImportArtifact(db Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("tool import requires an owner")
	}
	var t TempTool
	if err := json.Unmarshal(recipe, &t); err != nil {
		return "", "", fmt.Errorf("invalid tool recipe: %w", err)
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return "", "", Error("missing tool name")
	}
	// Lands in the pending pool for review. An already-active same-named tool
	// makes this return an error (delete-first), which we surface as a skip so
	// the rest of the bundle still imports.
	if err := QueuePendingTempTool(db, owner, t, "import"); err != nil {
		return name, err.Error(), nil
	}
	return name, "", nil
}

// ---- credential (API) ------------------------------------------------------

// credentialArtifact makes a SecureAPI credential portable. The SecureCredential
// struct is ALREADY secret-free by design — the sensitive material (client
// secret, refresh token, RSA key, api key) lives in a SEPARATE encrypted DB key,
// never in the struct — so the recipe is just the config with runtime/identity
// fields zeroed. On import the config lands as a DRAFT: DISABLED with a
// "(pending)" secret placeholder (via SaveAPIDraft / SaveOAuthDraft), so it is
// inert until the admin supplies the secret in Admin > APIs and enables it.
// Global (admin) scope — Owner is ignored. Per-user secrets never travel either;
// each install's users supply their own on their Account page.
type credentialArtifact struct{}

func (credentialArtifact) ArtifactType() string { return "credential" }

func (credentialArtifact) ListArtifacts(_ Database) []ArtifactSel {
	api := Secure()
	if api == nil {
		return nil
	}
	var out []ArtifactSel
	for _, c := range api.List() {
		out = append(out, ArtifactSel{Type: "credential", Name: c.Name})
	}
	return out
}

func (credentialArtifact) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	api := Secure()
	if api == nil {
		return nil, Error("secure-api store not initialized")
	}
	c, ok := api.Load(name)
	if !ok {
		return nil, fmt.Errorf("no credential named %q", name)
	}
	// Zero the fields that describe a particular install rather than the
	// credential's shape. No secret is present to strip — it was never here.
	c.CreatedAt = time.Time{}
	c.LastUsedAt = time.Time{}
	c.Pending = false
	c.Disabled = false
	return json.Marshal(c)
}

func (credentialArtifact) ImportArtifact(_ Database, recipe json.RawMessage, _ string) (string, string, error) {
	api := Secure()
	if api == nil {
		return "", "", Error("secure-api store not initialized")
	}
	var c SecureCredential
	if err := json.Unmarshal(recipe, &c); err != nil {
		return "", "", fmt.Errorf("invalid credential recipe: %w", err)
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return "", "", Error("missing credential name")
	}
	if _, exists := api.Load(name); exists {
		return name, "a credential with this name already exists", nil
	}
	c.CreatedAt = time.Time{}
	c.LastUsedAt = time.Time{}
	c.Pending = false
	// Land inert — no secret travels, so it can't dispatch until the admin
	// supplies one. oauth2 / key-style go through their draft-save; a no-auth
	// (url-allowlist-only) credential has no secret and saves directly.
	var err error
	switch c.Type {
	case SecureCredOAuth2:
		err = api.SaveOAuthDraft(c)
	case SecureCredNone:
		err = api.Save(c, "")
	default:
		err = api.SaveAPIDraft(c)
	}
	if err != nil {
		return name, "", err
	}
	return name, "", nil
}
