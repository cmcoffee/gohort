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
	RegisterArtifactType(skillArtifact{})
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

// Dependencies folds in the SecureAPI credential a connector reaches out
// through, so exporting a rest_poll / remote_mcp / desktop_mcp connector carries
// its credential (as a disabled draft) with it. Parsed generically from the
// Spec — see connectorCredentialRefs — so a new connector kind that names a
// credential the same way is covered without touching this type.
func (connectorArtifact) Dependencies(db Database, name, _ string) []ArtifactSel {
	c, ok := GetConnector(db, name)
	if !ok {
		return nil
	}
	return connectorSpecDeps(c.Spec)
}

// RecipeDependencies extracts the same credential references straight from a
// recipe, for import preview. A PortableConnector's Spec is the stored Spec
// verbatim, so both interfaces share connectorSpecDeps.
func (connectorArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, _ string, _ func(typ, name string) bool) []ArtifactSel {
	var pc PortableConnector
	if json.Unmarshal(recipe, &pc) != nil {
		return nil
	}
	return connectorSpecDeps(pc.Spec)
}

// connectorSpecDeps is the one walk behind both dependency interfaces: the
// exportable credential references a connector Spec names.
func connectorSpecDeps(spec json.RawMessage) []ArtifactSel {
	var out []ArtifactSel
	for _, cred := range connectorCredentialRefs(spec) {
		if exportableCredential(cred) {
			out = append(out, ArtifactSel{Type: "credential", Name: cred})
		}
	}
	return out
}

// connectorCredentialRefs extracts credential-name references from a connector
// Spec without knowing each kind's concrete struct. rest_poll spells it
// "credential"; the MCP kinds (remote_mcp / desktop_mcp) spell it "secure_cred".
// A generic probe over both keys keeps closure decoupled from the spec types.
func connectorCredentialRefs(spec json.RawMessage) []string {
	if len(spec) == 0 {
		return nil
	}
	var probe struct {
		Credential string `json:"credential"`
		SecureCred string `json:"secure_cred"`
	}
	if err := json.Unmarshal(spec, &probe); err != nil {
		return nil
	}
	var out []string
	if c := strings.TrimSpace(probe.Credential); c != "" {
		out = append(out, c)
	}
	if c := strings.TrimSpace(probe.SecureCred); c != "" {
		out = append(out, c)
	}
	return out
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
			return marshalExportedTool(p.Tool, owner)
		}
	}
	// Fall back to the pending pool so a not-yet-approved draft can still be
	// shared for review.
	for _, p := range LoadPendingTempTools(db, owner) {
		if p.Tool.Name == name {
			return marshalExportedTool(p.Tool, owner)
		}
	}
	return nil, fmt.Errorf("no tool named %q for user %q", name, owner)
}

// ResolveToolScriptForExport, when set, backfills a tool's ScriptBody from its
// on-disk workspace copy at export time. It exists for tools authored via
// local(write) + a {workspace_dir} command_template reference BEFORE the
// authoring path learned to capture the script into the record: their
// ScriptBody is empty, so a bundle export would carry no script. temptool wires
// this in init() (it owns the workspace + script-reference logic and would be a
// package cycle to call from here). No-op / unset leaves the tool untouched.
var ResolveToolScriptForExport func(t *TempTool, owner string)

// marshalExportedTool serializes a tool for a bundle, first backfilling a
// missing ScriptBody from disk (legacy tools) so the Python/bash script always
// travels with the export. Operates on a copy — the stored record is untouched.
func marshalExportedTool(t TempTool, owner string) (json.RawMessage, error) {
	if t.ScriptBody == "" && ResolveToolScriptForExport != nil {
		ResolveToolScriptForExport(&t, owner)
	}
	return json.Marshal(t)
}

// Dependencies folds in the API credential an api- / toolbox-mode tool
// dispatches through (TempTool.Credential is a name reference — the secret
// stays server-side). shell- and pipeline-mode tools carry their code inline
// and reference no credential, so they contribute none. The bootstrap no_auth
// sentinel is filtered by exportableCredential. Owner scopes the per-user tool
// store, same as ExportArtifact.
func (toolArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	t, ok := findOwnedTempTool(db, name, owner)
	if !ok {
		return nil
	}
	return tempToolCredDeps(t)
}

// RecipeDependencies extracts the same credential reference straight from a
// recipe (the recipe IS a TempTool), for import preview.
func (toolArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, _ string, _ func(typ, name string) bool) []ArtifactSel {
	var t TempTool
	if json.Unmarshal(recipe, &t) != nil {
		return nil
	}
	return tempToolCredDeps(t)
}

// tempToolCredDeps is the one walk behind both dependency interfaces: the
// exportable credential a tool dispatches through, if any.
func tempToolCredDeps(t TempTool) []ArtifactSel {
	cred := strings.TrimSpace(t.Credential)
	if !exportableCredential(cred) {
		return nil
	}
	return []ArtifactSel{{Type: "credential", Name: cred}}
}

// findOwnedTempTool resolves the named temp tool owned by owner, checking the
// persistent (admin-approved) pool first and then the pending queue — the same
// order ExportArtifact uses so a tool and its dependency walk agree on which
// record they mean. Shared by the tool artifact's own dependency walk and by
// cross-type references (an agent that allowlists the tool by name).
func findOwnedTempTool(db Database, name, owner string) (TempTool, bool) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if owner == "" || name == "" {
		return TempTool{}, false
	}
	for _, p := range LoadPersistentTempTools(db, owner) {
		if p.Tool.Name == name {
			return p.Tool, true
		}
	}
	for _, p := range LoadPendingTempTools(db, owner) {
		if p.Tool.Name == name {
			return p.Tool, true
		}
	}
	return TempTool{}, false
}

// IsExportableTool reports whether name resolves to a temp tool owned by owner
// that the "tool" artifact type can export. It is the predicate another
// artifact type uses to declare a dependency on a tool it references by name
// (an agent's allowlist) without duplicating the store lookup or reaching into
// tool internals — built-in registered tools and unknown names return false.
func IsExportableTool(db Database, name, owner string) bool {
	_, ok := findOwnedTempTool(db, name, owner)
	return ok
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

// ---- skill -------------------------------------------------------------

// skillArtifact makes a SkillRecord portable. The recipe is the record with
// identity stripped: ID / Owner / timestamps zeroed (import mints a fresh ID
// under the importing owner) and the legacy Embedding cache dropped. Bundled
// Tools ([]TempTool) travel INLINE — being self-contained is the point of
// SkillRecord.Tools — with the same on-disk ScriptBody backfill legacy tools
// get on export. AttachedCollections travel as ID references: the corpus
// itself is not portable (yet), so import keeps the references and the
// dependency pass warns that the collections aren't present.
//
// Per-user scope like tools. Import lands the skill DISABLED: a skill injects
// prompt instructions, and its bundled tools load straight into the session
// once the skill is consulted (consulting an allowed skill IS the opt-in — no
// pending-pool gate), so the human review point has to be the admin reading
// the skill and enabling it.
type skillArtifact struct{}

func (skillArtifact) ArtifactType() string { return "skill" }

func (skillArtifact) ListArtifacts(db Database) []ArtifactSel {
	store := skillStore(db)
	if store == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range store.Keys(skillsTable) {
		for _, s := range LoadSkills(db, u) {
			out = append(out, ArtifactSel{Type: "skill", Name: s.Name, Owner: u})
		}
	}
	return out
}

func (skillArtifact) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("skill export requires an owner")
	}
	s, ok := FindSkillByName(db, owner, name)
	if !ok {
		return nil, fmt.Errorf("no skill named %q for user %q", name, owner)
	}
	// Strip identity — the importing install mints its own ID / owner /
	// timestamps — and the Embedding cache (dead weight since activation went
	// LLM-driven). Disabled is a local mute, not part of the skill's shape.
	s.ID = ""
	s.Owner = ""
	s.Disabled = false
	s.Embedding = nil
	s.Created = time.Time{}
	s.Updated = time.Time{}
	// Bundled tools ship their scripts inline; backfill any legacy tool whose
	// script still lives only in the on-disk workspace (same rule as the tool
	// artifact's marshalExportedTool). LoadSkills decodes a fresh copy, so
	// mutating here never touches the stored record.
	for i := range s.Tools {
		if s.Tools[i].ScriptBody == "" && ResolveToolScriptForExport != nil {
			ResolveToolScriptForExport(&s.Tools[i], owner)
		}
	}
	return json.Marshal(s)
}

// Dependencies folds in what the skill references by name: registered temp
// tools from its AllowedTools allowlist (built-ins and source-hooks fail
// IsExportableTool and are skipped), the SecureAPI credentials its bundled
// tools dispatch through, and its attached collections. No "collection"
// artifact type is registered today, so export closure silently drops those
// and import warns the corpus isn't present — honest now, and the closure
// starts carrying it the day collections become portable. The well-known
// deployment-knowledge collection exists on every install, so it is never a
// dependency (same rule as the bootstrap no_auth credential).
func (skillArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	s, ok := FindSkillByName(db, strings.TrimSpace(owner), name)
	if !ok {
		return nil
	}
	return skillRecipeDeps(db, s, owner, nil)
}

// RecipeDependencies extracts the same references straight from a recipe (the
// recipe IS a SkillRecord), for import preview. inBundle lets an allowlisted
// tool traveling in the same bundle count as a portable-tool reference even
// though it isn't in the store yet.
func (skillArtifact) RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	var s SkillRecord
	if json.Unmarshal(recipe, &s) != nil {
		return nil
	}
	return skillRecipeDeps(db, s, owner, inBundle)
}

// skillRecipeDeps is the one walk behind both dependency interfaces. An
// allowlisted name is a tool reference when it resolves to an exportable temp
// tool on this install or (recipe path) travels in the bundle under preview —
// built-ins and source-hook names fail both and are skipped.
func skillRecipeDeps(db Database, s SkillRecord, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	seen := map[string]bool{}
	var out []ArtifactSel
	add := func(typ, name, owner string) {
		key := typ + "\x00" + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ArtifactSel{Type: typ, Name: name, Owner: owner})
	}
	for _, tn := range s.AllowedTools {
		tn = strings.TrimSpace(tn)
		if IsExportableTool(db, tn, owner) || (inBundle != nil && inBundle("tool", tn)) {
			add("tool", tn, owner)
		}
	}
	for _, bt := range s.Tools {
		if cred := strings.TrimSpace(bt.Credential); exportableCredential(cred) {
			add("credential", cred, "")
		}
	}
	for _, cid := range s.AttachedCollections {
		if cid = strings.TrimSpace(cid); cid != DeploymentKnowledgeCollectionID {
			add("collection", cid, "")
		}
	}
	return out
}

func (skillArtifact) ImportArtifact(db Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("skill import requires an owner")
	}
	var s SkillRecord
	if err := json.Unmarshal(recipe, &s); err != nil {
		return "", "", fmt.Errorf("invalid skill recipe: %w", err)
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return "", "", Error("missing skill name")
	}
	if _, exists := FindSkillByName(db, owner, name); exists {
		return name, "a skill with this name already exists", nil
	}
	// Fresh identity under the importing owner (SaveSkill assigns ID + stamps),
	// landed DISABLED so nothing an import brought in can steer a conversation
	// or surface its bundled tools before the admin reviews and enables it.
	s.ID = ""
	s.Embedding = nil
	s.Disabled = true
	if _, err := SaveSkill(db, owner, s); err != nil {
		return name, "", err
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
	// Land inert — every imported credential is a draft the admin reviews and
	// enables. oauth2 / key-style go through their draft-save (which forces
	// disabled + a "(pending)" secret placeholder); a no-auth (url-allowlist-
	// only) credential has no secret, so we save it directly but still
	// DISABLED so nothing an import brought in can dispatch unreviewed.
	c.Disabled = true
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
