// Artifact-type registrations: the concrete ArtifactType implementations that
// make each store portable through the unified bundle (core/artifact_pack.go).
// Each wraps that type's EXISTING export/import logic — the registry adds
// portability without the underlying stores knowing bundles exist. Adding a new
// portable type (agent, credential, pipeline) is one more implementation and one
// more RegisterArtifactType line here; the envelope, endpoints, and UI don't
// change.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterArtifactType(connectorArtifact{})
	RegisterArtifactType(toolArtifact{})
	RegisterArtifactType(credentialArtifact{})
	RegisterArtifactType(skillArtifact{})
	RegisterArtifactType(collectionArtifact{})
	RegisterArtifactType(customAppArtifact{})
	RegisterArtifactType(sourceHookArtifact{})
	RegisterArtifactType(monitorArtifact{})
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
		Name:     name,
		Kind:     strings.TrimSpace(pc.Kind),
		Desc:     pc.Desc,
		Template: strings.TrimSpace(pc.Template),
		Spec:     pc.Spec,
		Owner:    owner,
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

// findSkillForExport resolves a skill by ID first, then by case-insensitive
// name. ID-first matters: cross-artifact references (an agent's AllowedSkills)
// are IDs, so the dependency closure and the existence probe address skills
// the same way humans' export buttons address them by name.
func findSkillForExport(db Database, owner, nameOrID string) (SkillRecord, bool) {
	id := strings.TrimSpace(nameOrID)
	if id == "" {
		return SkillRecord{}, false
	}
	for _, s := range LoadSkills(db, owner) {
		if s.ID == id {
			return s, true
		}
	}
	return FindSkillByName(db, owner, id)
}

func (skillArtifact) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("skill export requires an owner")
	}
	s, ok := findSkillForExport(db, owner, name)
	if !ok {
		return nil, fmt.Errorf("no skill named %q for user %q", name, owner)
	}
	// Strip owner + timestamps — the importing install reassigns them — and
	// the Embedding cache (dead weight since activation went LLM-driven).
	// Disabled is a local mute, not part of the skill's shape. ID TRAVELS
	// (same rule as collections/pipelines): it's the key an agent's
	// AllowedSkills references, so preserving it is what lets an agent+skill
	// bundle land with its wiring intact.
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
// tools from its AllowedTools allowlist, source hooks behind hook-backed
// tool names in that same allowlist (built-ins fail every probe and are
// skipped), the SecureAPI credentials its bundled tools dispatch through,
// and its attached collections. The well-known deployment-knowledge
// collection exists on every install, so it is never a dependency (same
// rule as the bootstrap no_auth credential).
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
// tool on this install or (recipe path) travels in the bundle under preview;
// a name backed by a source hook (pubmed_search) is a source_hook reference,
// so the hook the skill was built to search travels too. Built-in names fail
// every probe and are skipped.
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
		} else if h, ok := FindSourceHookByToolName(tn); ok {
			add("source_hook", h.Name, "")
		}
	}
	for _, bt := range s.Tools {
		if cred := strings.TrimSpace(bt.Credential); exportableCredential(cred) {
			add("credential", cred, "")
		}
	}
	// Collection references carry the skill's owner: collections are per-user
	// artifacts, so the closure/probe resolves them through the owner's pool
	// (an owner-less collection selector can't be exported).
	for _, cid := range s.AttachedCollections {
		if cid = strings.TrimSpace(cid); cid != DeploymentKnowledgeCollectionID {
			add("collection", cid, owner)
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
	// The traveled ID is preserved (it's what agents in the same bundle
	// reference via AllowedSkills), so a same-ID skill skips, same as a name
	// collision. A legacy recipe without an ID gets a fresh one from
	// SaveSkill. Either way the skill lands DISABLED so nothing an import
	// brought in can steer a conversation or surface its bundled tools before
	// the admin reviews and enables it.
	if id := strings.TrimSpace(s.ID); id != "" {
		for _, ex := range LoadSkills(db, owner) {
			if ex.ID == id {
				return name, "a skill with this id already exists", nil
			}
		}
	}
	s.Embedding = nil
	s.Disabled = true
	if _, err := SaveSkill(db, owner, s); err != nil {
		return name, "", err
	}
	return name, "", nil
}

// ---- collection --------------------------------------------------------

// PortableCollection is a Document Collection's wire recipe: metadata plus
// every chunk's TEXT — vectors deliberately do not travel. Embeddings are
// tied to the exporting install's embedding model and dominate the payload
// size, so the importing install re-embeds with its own model instead
// (background pass; see ingestImportedCollectionChunks).
//
// ID is the one identity field that DOES travel (a rule collections
// established, now shared with skills and pipelines): a collection's ID is
// its cross-artifact reference key (skills' AttachedCollections, agents'
// knowledge pickers), so preserving it is what lets a skill+collection
// bundle land with its wiring intact.
type PortableCollection struct {
	ID                 string          `json:"id,omitempty"`
	Name               string          `json:"name"`
	Description        string          `json:"description,omitempty"`
	FilterRules        string          `json:"filter_rules,omitempty"`
	ClassifyOnAutofill bool            `json:"classify_on_autofill,omitempty"`
	IngestedURLs       []string        `json:"ingested_urls,omitempty"`
	Chunks             []PortableChunk `json:"chunks,omitempty"`
}

// PortableChunk is one EmbeddedChunk minus everything install-specific: no
// chunk ID (fresh UUIDs on import), no Source tag (derived from the
// collection ID), no Vector/Model (re-embedded on import).
type PortableChunk struct {
	ReportID string `json:"report_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Section  string `json:"section,omitempty"`
	Text     string `json:"text"`
	Date     string `json:"date,omitempty"`
	Locator  string `json:"locator,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// collectionArtifact makes a Document Collection portable — the first
// artifact type that carries DATA (a corpus) rather than a recipe for a
// capability. Export = metadata + chunk text without vectors; import =
// user-scoped collection under the importing owner, with chunks re-embedded
// in the BACKGROUND so a large corpus doesn't stall the import request
// (until the pass finishes — or when no embedding backend is configured —
// the chunks are still keyword-searchable; vector search skips rows without
// a matching vector).
//
// ListArtifacts enumerates user-scoped collections only: deployment-scoped
// ones (notably the auto-populated deployment-knowledge corpus, which can be
// enormous) never ride along in an "export all", though an explicit
// per-collection export still resolves them.
type collectionArtifact struct{}

func (collectionArtifact) ArtifactType() string { return "collection" }

func (collectionArtifact) ListArtifacts(_ Database) []ArtifactSel {
	base := CollectionsDB()
	authDB := AuthDB()
	if base == nil || authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(base, u.Username)
		if udb == nil {
			continue
		}
		for _, k := range udb.Keys(CollectionsTable) {
			var c Collection
			if !udb.Get(CollectionsTable, k, &c) {
				continue
			}
			if c.Owner != u.Username || IsDeploymentScope(c) {
				continue
			}
			out = append(out, ArtifactSel{Type: "collection", Name: c.Name, Owner: u.Username})
		}
	}
	return out
}

// findCollectionForExport resolves a collection by ID first, then by
// case-insensitive name. ID-first matters: cross-artifact references (a
// skill's AttachedCollections) are IDs, so the dependency closure and the
// existence probe address collections the same way humans' export buttons
// address them by name.
func findCollectionForExport(owner, nameOrID string) (Collection, bool) {
	udb := UserDB(CollectionsDB(), owner)
	if c, ok := LoadCollection(udb, owner, strings.TrimSpace(nameOrID)); ok {
		return c, true
	}
	lower := strings.ToLower(strings.TrimSpace(nameOrID))
	if lower == "" {
		return Collection{}, false
	}
	for _, c := range ListCollections(udb, owner) {
		if strings.ToLower(strings.TrimSpace(c.Name)) == lower {
			return c, true
		}
	}
	return Collection{}, false
}

// collectionChunks reads every chunk under the collection's source tag from
// the store searches actually use: the dedicated VectorDB, falling back to
// the legacy split homes (collections bucket root for user-scoped, RootDB
// for deployment) only when VectorDB isn't up. Sorted for byte-stable
// exports (snapshot order is cache order, not deterministic).
func collectionChunks(c Collection) []EmbeddedChunk {
	src := CollectionSource(c.ID)
	var out []EmbeddedChunk
	if VectorDB != nil {
		out = ChunksForSource(VectorDB, src)
	} else {
		if base := CollectionsDB(); base != nil {
			out = append(out, ChunksForSource(base, src)...)
		}
		if RootDB != nil {
			out = append(out, ChunksForSource(RootDB, src)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReportID != out[j].ReportID {
			return out[i].ReportID < out[j].ReportID
		}
		if out[i].Section != out[j].Section {
			return out[i].Section < out[j].Section
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (collectionArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("collection export requires an owner")
	}
	c, ok := findCollectionForExport(owner, name)
	if !ok {
		return nil, fmt.Errorf("no collection named %q for user %q", name, owner)
	}
	pc := PortableCollection{
		ID:                 c.ID,
		Name:               c.Name,
		Description:        c.Description,
		FilterRules:        c.FilterRules,
		ClassifyOnAutofill: c.ClassifyOnAutofill,
		IngestedURLs:       c.IngestedURLs,
	}
	for _, ch := range collectionChunks(c) {
		pc.Chunks = append(pc.Chunks, PortableChunk{
			ReportID: ch.ReportID,
			Title:    ch.Title,
			Section:  ch.Section,
			Text:     ch.Text,
			Date:     ch.Date,
			Locator:  ch.Locator,
			Kind:     ch.Kind,
		})
	}
	return json.Marshal(pc)
}

func (collectionArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("collection import requires an owner")
	}
	var pc PortableCollection
	if err := json.Unmarshal(recipe, &pc); err != nil {
		return "", "", fmt.Errorf("invalid collection recipe: %w", err)
	}
	name := strings.TrimSpace(pc.Name)
	if name == "" {
		return "", "", Error("missing collection name")
	}
	base := CollectionsDB()
	if base == nil {
		return name, "", Error("collections store not initialized")
	}
	udb := UserDB(base, owner)
	// The recipe's ID is preserved (it's what skills in the same bundle
	// reference), so same-ID — including a deployment-scoped one like
	// deployment-knowledge — skips, same as a name collision.
	id := strings.TrimSpace(pc.ID)
	if id == "" {
		id = UUIDv4()
	}
	if _, exists := LoadCollection(udb, owner, id); exists {
		return name, "a collection with this id already exists", nil
	}
	for _, c := range ListCollections(udb, owner) {
		if strings.EqualFold(strings.TrimSpace(c.Name), name) {
			return name, "a collection with this name already exists", nil
		}
	}
	c := Collection{
		ID:                 id,
		Owner:              owner,
		Name:               name,
		Description:        pc.Description,
		FilterRules:        pc.FilterRules,
		ClassifyOnAutofill: pc.ClassifyOnAutofill,
		IngestedURLs:       pc.IngestedURLs,
		Created:            time.Now(),
	}
	SaveCollection(udb, c) // always user-scoped on import; admin can re-scope locally
	if len(pc.Chunks) > 0 {
		go ingestImportedCollectionChunks(id, name, pc.Chunks)
	}
	return name, "", nil
}

// ingestImportedCollectionChunks is the background re-embed pass behind a
// collection import: each traveled chunk keeps its text and shape but gets a
// fresh ID, this install's embedding, and the collection's source tag. Runs
// off the request goroutine — embedding a large corpus can take minutes.
// Chunks that fail to embed (or arrive with no backend configured) are
// stored WITHOUT a vector: keyword search still reaches them, and a future
// re-embed pass can fill the gap; dropping the text would be data loss.
func ingestImportedCollectionChunks(id, name string, chunks []PortableChunk) {
	db := VectorDB
	if db == nil {
		db = CollectionsDB() // legacy pre-VectorDB home for user-scoped chunks
	}
	if db == nil {
		Log("[artifacts] collection %q: no chunk store available; %d chunk(s) not ingested", name, len(chunks))
		return
	}
	cfg := GetEmbeddingConfig()
	src := CollectionSource(id)
	now := time.Now().Format(time.RFC3339)
	var embedded, empty int
	for _, pc := range chunks {
		text := strings.TrimSpace(pc.Text)
		if text == "" {
			continue
		}
		var vec []float32
		if cfg.Enabled {
			if v, err := EmbedWith(context.Background(), cfg, text); err == nil {
				vec = v
			}
		}
		if len(vec) > 0 {
			embedded++
		} else {
			empty++
		}
		date := strings.TrimSpace(pc.Date)
		if date == "" {
			date = now
		}
		row := EmbeddedChunk{
			ID:       UUIDv4(),
			Source:   src,
			ReportID: pc.ReportID,
			Title:    pc.Title,
			Section:  pc.Section,
			Text:     text,
			Vector:   vec,
			Model:    cfg.Model,
			Date:     date,
			Locator:  pc.Locator,
			Kind:     pc.Kind,
		}
		db.Set(EmbeddedChunks, row.ID, row)
	}
	InvalidateChunkCache()
	Log("[artifacts] collection %q: ingested %d imported chunk(s) (%d embedded, %d without vectors)",
		name, embedded+empty, embedded, empty)
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

// ---- custom app --------------------------------------------------------

// ResolveAgentNameForExport, when set, resolves an agent reference (ID or
// name) in owner's store to the agent's canonical NAME. It exists because a
// custom app binds its chat agent by ID (AppSpec.AgentID), but an agent
// recipe is reborn under a fresh ID on import — only the name survives the
// trip, so export normalizes the reference. Agents live in orchestrate's
// per-user store, which core can't reach; orchestrate wires this from
// Routes(). Unset / unresolvable leaves the reference untouched (best-effort,
// same rule as every dependency walk).
var ResolveAgentNameForExport func(owner, key string) (string, bool)

// customAppArtifact makes a custom app (AppSpec) portable: the declarative
// page, record-store shape, and inline data-source/action scripts travel as
// one recipe; the bound chat agent and any fetch_via credentials the scripts
// name ride along via the dependency closure. The spec store is core-side
// (RootDB user:<owner>), so this registers from init() like skills — no app
// capture needed.
//
// Import lands DISABLED: a spec can carry sandboxed scripts, and the Custom
// Apps index's Enable button is the review gate (same posture as skills'
// disabled draft). Records never travel — a recipe is the app's shape, not
// its data.
type customAppArtifact struct{}

func (customAppArtifact) ArtifactType() string { return "custom_app" }

func (customAppArtifact) ListArtifacts(_ Database) []ArtifactSel {
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		for _, s := range ListAppSpecs(u.Username) {
			out = append(out, ArtifactSel{Type: "custom_app", Name: s.Slug, Owner: u.Username})
		}
	}
	return out
}

// findAppSpecForExport resolves a custom app by slug first (the storage key
// and the identity ListArtifacts emits), then by case-insensitive display
// name (the recipe's "name" field, which the preview's probes use).
func findAppSpecForExport(owner, slugOrName string) (AppSpec, bool) {
	key := strings.TrimSpace(slugOrName)
	if key == "" {
		return AppSpec{}, false
	}
	if s, ok := LoadAppSpec(owner, key); ok {
		return s, true
	}
	lower := strings.ToLower(key)
	for _, s := range ListAppSpecs(owner) {
		if strings.ToLower(strings.TrimSpace(s.Name)) == lower {
			return s, true
		}
	}
	return AppSpec{}, false
}

func (customAppArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("custom-app export requires an owner")
	}
	spec, ok := findAppSpecForExport(owner, name)
	if !ok {
		return nil, fmt.Errorf("no custom app %q for user %q", name, owner)
	}
	// Normalize the bound agent ref to the agent's NAME (see
	// ResolveAgentNameForExport). Owner/timestamps are reassigned on import;
	// Disabled is a local mute, not part of the app's shape.
	if ref := strings.TrimSpace(spec.AgentID); ref != "" && ResolveAgentNameForExport != nil {
		if n, resolved := ResolveAgentNameForExport(owner, ref); resolved {
			spec.AgentID = n
		}
	}
	spec.Owner = ""
	spec.Created = ""
	spec.Updated = ""
	spec.Disabled = false
	// Sharing is deployment-local and owner-scoped (a shared-slug registration /
	// a live capability token in THIS deployment). It must never travel in a
	// bundle: the importer re-shares on their own terms.
	spec.Shared = false
	spec.PublicToken = ""
	return json.Marshal(spec)
}

// Dependencies folds in what the app references: the chat agent it binds
// (normalized to a name, so the emission matches what the recipe carries) and
// the SecureAPI credentials its data-source/action scripts dispatch through
// (the fetch_via:<cred> sandbox capability). Both are emitted unconditionally
// — a missing agent or credential should warn at import, not vanish.
func (customAppArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil
	}
	spec, ok := findAppSpecForExport(owner, name)
	if !ok {
		return nil
	}
	if ref := strings.TrimSpace(spec.AgentID); ref != "" && ResolveAgentNameForExport != nil {
		if n, resolved := ResolveAgentNameForExport(owner, ref); resolved {
			spec.AgentID = n
		}
	}
	return customAppRecipeDeps(spec, owner)
}

// RecipeDependencies extracts the same references straight from a recipe (the
// recipe IS an AppSpec), for import preview. The agent ref is already
// normalized by export, so no store resolution is needed.
func (customAppArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var spec AppSpec
	if json.Unmarshal(recipe, &spec) != nil {
		return nil
	}
	return customAppRecipeDeps(spec, strings.TrimSpace(owner))
}

// customAppRecipeDeps is the one walk behind both dependency interfaces: the
// bound chat agent plus every fetch_via credential named by a data source or
// action's sandbox capabilities. Bootstrap credential sentinels are filtered,
// same as everywhere.
func customAppRecipeDeps(spec AppSpec, owner string) []ArtifactSel {
	seen := map[string]bool{}
	var out []ArtifactSel
	add := func(typ, name, o string) {
		key := typ + "\x00" + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ArtifactSel{Type: typ, Name: name, Owner: o})
	}
	if ref := strings.TrimSpace(spec.AgentID); ref != "" {
		add("agent", ref, owner)
	}
	capCreds := func(caps []string) {
		for _, c := range caps {
			c = strings.TrimSpace(c)
			if !strings.HasPrefix(c, "fetch_via:") {
				continue
			}
			if cred := strings.TrimSpace(strings.TrimPrefix(c, "fetch_via:")); exportableCredential(cred) {
				add("credential", cred, "")
			}
		}
	}
	for _, ds := range spec.DataSources {
		capCreds(ds.Capabilities)
	}
	for _, ac := range spec.Actions {
		capCreds(ac.Capabilities)
	}
	return out
}

// ImportArtifact reconstitutes a custom app under owner, DISABLED — the spec
// can carry sandboxed scripts, so nothing runs until the owner reviews and
// enables it from the Custom Apps index. A same-slug app already in owner's
// store skips, never clobbered. Records don't travel, so the imported app
// starts empty.
func (customAppArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("custom-app import requires an owner")
	}
	var spec AppSpec
	if err := json.Unmarshal(recipe, &spec); err != nil {
		return "", "", fmt.Errorf("invalid custom-app recipe: %w", err)
	}
	slug := strings.TrimSpace(spec.Slug)
	if slug == "" {
		return strings.TrimSpace(spec.Name), "", Error("missing app slug")
	}
	if appSpecStore(owner) == nil {
		return slug, "", Error("app spec store not initialized")
	}
	if _, exists := LoadAppSpec(owner, slug); exists {
		return slug, "an app with this slug already exists", nil
	}
	spec.Slug = slug
	spec.Owner = owner
	spec.Created = ""
	spec.Updated = ""
	spec.Disabled = true
	// An imported app is never pre-shared, even if a hand-crafted bundle set the
	// flags — the importer shares on their own terms (and the token would be
	// meaningless in this deployment's index anyway).
	spec.Shared = false
	spec.PublicToken = ""
	SaveAppSpec(spec)
	return slug, "", nil
}

// ---- source hook -------------------------------------------------------

// sourceHookArtifact makes a SourceHook portable: endpoint, response-shape
// mapping, trigger routing, and LLM-tool exposure travel as one recipe.
// Global (admin) scope, like connectors — Owner is ignored. The hook store
// is core-side (RootDB sourceHookTable, mirrored into the in-memory
// registry), so this registers from init().
//
// SECRETS NEVER TRAVEL: AuthKey is cleared on export (it's stored encrypted
// and separately anyway), and export REFUSES a hook whose Endpoint looks
// like it hardcodes a key in the URL instead — the same rule BuildTemplate
// applies to tools. Import lands DISABLED (an active hook receives live
// search queries at its endpoint — topic routing, paywall matching, LLM
// tools all consult it); the admin reviews, supplies the auth key if any,
// and enables it from Admin > Source Hooks.
type sourceHookArtifact struct{}

func (sourceHookArtifact) ArtifactType() string { return "source_hook" }

func (sourceHookArtifact) ListArtifacts(_ Database) []ArtifactSel {
	var out []ArtifactSel
	for _, h := range RegisteredSourceHooks() {
		out = append(out, ArtifactSel{Type: "source_hook", Name: h.Name})
	}
	return out
}

// findSourceHookForExport resolves a hook by case-insensitive display name —
// hooks are keyed by name, there is no separate ID.
func findSourceHookForExport(name string) (SourceHook, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SourceHook{}, false
	}
	for _, h := range RegisteredSourceHooks() {
		if strings.EqualFold(strings.TrimSpace(h.Name), name) {
			return h, true
		}
	}
	return SourceHook{}, false
}

func (sourceHookArtifact) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	h, ok := findSourceHookForExport(name)
	if !ok {
		return nil, fmt.Errorf("no source hook named %q", name)
	}
	if scanForEmbeddedSecret(h.Endpoint) {
		return nil, fmt.Errorf("source hook %q looks like it hardcodes a key in its endpoint URL; move it into the hook's auth key before exporting", h.Name)
	}
	// AuthKey is the secret — it never travels. Disabled is a local mute.
	h.AuthKey = ""
	h.Disabled = false
	return json.Marshal(h)
}

// ImportArtifact reconstitutes a hook DISABLED and secret-free: whatever a
// recipe claims as AuthKey is dropped (a traveled secret is either a leak or
// a lie), and the hook receives no traffic until the admin enables it. A
// same-named hook already configured skips, never clobbered — SaveSourceHook
// overwrites by name, so the collision check here is what keeps import
// non-destructive.
func (sourceHookArtifact) ImportArtifact(db Database, recipe json.RawMessage, _ string) (string, string, error) {
	var h SourceHook
	if err := json.Unmarshal(recipe, &h); err != nil {
		return "", "", fmt.Errorf("invalid source-hook recipe: %w", err)
	}
	name := strings.TrimSpace(h.Name)
	if name == "" {
		return "", "", Error("missing source hook name")
	}
	if _, exists := findSourceHookForExport(name); exists {
		return name, "a source hook with this name already exists", nil
	}
	h.AuthKey = ""
	h.Disabled = true
	SaveSourceHook(db, h)
	return name, "", nil
}

// ---- monitor (event monitor) --------------------------------------------

// monitorArtifact makes an EventMonitor portable — the standing trigger that
// watches something (a tool's output, an HTTP value, an LLM-checked
// condition, a webhook) and wakes an agent when it fires. This closes the
// integration-bridge gap: a bridge is "credential + watch monitor", and until
// now a bundle could carry the connector/credential/tool half but the monitor
// arrived nowhere. The dependency walk pulls the watch tool (which pulls its
// credential) and the checker/wake agents, so "export this monitor" produces
// the whole bridge.
//
// EventMonitor is today's LIVE trigger record; the unified ScheduledTrigger
// engine (core/trigger.go) has no authoring surface yet. When that migration
// completes, this type's recipe rules (state stripped, token re-minted,
// paused import) transfer to it directly.
//
// The recipe is the WHEN+GATE+ACTION shape only. Owner, the webhook Token
// (re-minted on import — a traveled secret is either a leak or a lie), the
// edge-trigger state (hashes, baselines, breach/match flags), scheduler
// bookkeeping, and the instance-local delivery targets (WakeSession,
// DeliverChatID — session/chat IDs that mean nothing elsewhere) never
// travel. Agent references (CheckAgent / WakeAgent) are normalized ID→name
// on export, same as pipelines and custom apps. One-shot monitors are
// session-bound awaits, not reusable recipes — they neither list nor export.
//
// Import lands PAUSED: the existing console pause/resume surface is the
// review gate, and resume is what schedules it. Webhook monitors get a fresh
// token at import so they work the moment they're enabled.
type monitorArtifact struct{}

func (monitorArtifact) ArtifactType() string { return "monitor" }

func (monitorArtifact) ListArtifacts(db Database) []ArtifactSel {
	if db == nil {
		return nil
	}
	var out []ArtifactSel
	for _, k := range db.Keys(eventMonitorsTable) {
		var m EventMonitor
		if !db.Get(eventMonitorsTable, k, &m) || m.OneShot {
			continue
		}
		out = append(out, ArtifactSel{Type: "monitor", Name: m.Name, Owner: m.Owner})
	}
	return out
}

// findMonitorForExport resolves a monitor by exact name first (the storage
// key), then case-insensitively.
func findMonitorForExport(db Database, owner, name string) (EventMonitor, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return EventMonitor{}, false
	}
	if m, ok := GetEventMonitor(db, owner, name); ok {
		return m, true
	}
	lower := strings.ToLower(name)
	for _, m := range ListEventMonitors(db, owner) {
		if strings.ToLower(strings.TrimSpace(m.Name)) == lower {
			return m, true
		}
	}
	return EventMonitor{}, false
}

// normalizeMonitorAgents rewrites the checker/wake agent references to agent
// NAMES when they hold IDs (see ResolveAgentNameForExport) — the runtime
// resolves either form, but an imported agent is reborn under a fresh ID.
func normalizeMonitorAgents(m EventMonitor, owner string) EventMonitor {
	if ResolveAgentNameForExport == nil {
		return m
	}
	if ref := strings.TrimSpace(m.CheckAgent); ref != "" {
		if n, ok := ResolveAgentNameForExport(owner, ref); ok {
			m.CheckAgent = n
		}
	}
	if ref := strings.TrimSpace(m.WakeAgent); ref != "" {
		if n, ok := ResolveAgentNameForExport(owner, ref); ok {
			m.WakeAgent = n
		}
	}
	return m
}

func (monitorArtifact) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("monitor export requires an owner")
	}
	m, ok := findMonitorForExport(db, owner, name)
	if !ok {
		return nil, fmt.Errorf("no monitor named %q for user %q", name, owner)
	}
	if m.OneShot {
		return nil, fmt.Errorf("monitor %q is a one-shot await bound to its session — not a reusable recipe", m.Name)
	}
	// Refuse a recipe that would smuggle a literal secret: the polled URL and
	// the format script travel verbatim, and tool args are passed to the
	// watch tool on every fire.
	argsJSON, _ := json.Marshal(m.ToolArgs)
	for field, val := range map[string]string{
		"url": m.URL, "format_script": m.FormatScript, "tool_args": string(argsJSON),
	} {
		if scanForEmbeddedSecret(val) {
			return nil, fmt.Errorf("monitor %q looks like it hardcodes a secret in %s; route it through a credential before exporting", m.Name, field)
		}
	}
	m = normalizeMonitorAgents(m, owner)
	m.Owner = ""
	m.Token = ""
	m.WakeSession = ""
	m.DeliverChatID = ""
	m.LastHash = ""
	m.LastBody = ""
	m.LastResult = ""
	m.LastBreached = false
	m.LastMatched = false
	m.Paused = false
	m.Created = time.Time{}
	m.NextCheck = time.Time{}
	m.LastFired = time.Time{}
	m.LastChecked = time.Time{}
	m.SchedulerID = ""
	return json.Marshal(m)
}

// Dependencies folds in what the monitor references: the watch tool it
// invokes (or the source hook behind a hook-backed tool name) and the
// checker/wake agents — so exporting a monitor carries the whole bridge
// (monitor → tool → credential) it was built from.
func (monitorArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil
	}
	m, ok := findMonitorForExport(db, owner, name)
	if !ok {
		return nil
	}
	return monitorRecipeDeps(db, normalizeMonitorAgents(m, owner), owner, nil)
}

// RecipeDependencies extracts the same references straight from a recipe (the
// recipe IS an EventMonitor), for import preview.
func (monitorArtifact) RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	var m EventMonitor
	if json.Unmarshal(recipe, &m) != nil {
		return nil
	}
	return monitorRecipeDeps(db, m, strings.TrimSpace(owner), inBundle)
}

// monitorRecipeDeps is the one walk behind both dependency interfaces: the
// watch tool (an exportable temp tool, or the source hook behind a
// hook-backed name — built-ins fail every probe and are skipped) plus the
// checker and wake agents, emitted unconditionally so a missing agent warns
// at import instead of vanishing.
func monitorRecipeDeps(db Database, m EventMonitor, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	if owner == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []ArtifactSel
	add := func(typ, name, o string) {
		key := typ + "\x00" + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ArtifactSel{Type: typ, Name: name, Owner: o})
	}
	if tn := strings.TrimSpace(m.ToolName); tn != "" {
		if IsExportableTool(db, tn, owner) || (inBundle != nil && inBundle("tool", tn)) {
			add("tool", tn, owner)
		} else if h, ok := FindSourceHookByToolName(tn); ok {
			add("source_hook", h.Name, "")
		}
	}
	if a := strings.TrimSpace(m.CheckAgent); a != "" {
		add("agent", a, owner)
	}
	if a := strings.TrimSpace(m.WakeAgent); a != "" {
		add("agent", a, owner)
	}
	return out
}

// ImportArtifact reconstitutes a monitor under owner, PAUSED and with fresh
// local state: no edge baselines, no scheduler booking, and — for a webhook
// monitor — a freshly minted token, so the recipe's WHEN+GATE+ACTION shape is
// all that survives the trip. A same-named monitor already in owner's store
// skips, never clobbered. Resume (the existing console surface) is both the
// review gate and what schedules it.
func (monitorArtifact) ImportArtifact(db Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("monitor import requires an owner")
	}
	var m EventMonitor
	if err := json.Unmarshal(recipe, &m); err != nil {
		return "", "", fmt.Errorf("invalid monitor recipe: %w", err)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return "", "", Error("missing monitor name")
	}
	if m.OneShot {
		return name, "", fmt.Errorf("monitor %q is a one-shot await — not importable", name)
	}
	if _, exists := GetEventMonitor(db, owner, name); exists {
		return name, "a monitor with this name already exists", nil
	}
	m.Owner = owner
	m.Paused = true
	m.Token = ""
	if m.Kind == EventKindWebhook {
		m.Token = NewEventToken()
	}
	m.WakeSession = ""
	m.DeliverChatID = ""
	m.LastHash = ""
	m.LastBody = ""
	m.LastResult = ""
	m.LastBreached = false
	m.LastMatched = false
	m.NextCheck = time.Time{}
	m.LastFired = time.Time{}
	m.LastChecked = time.Time{}
	m.SchedulerID = ""
	m.Created = time.Now()
	SaveEventMonitor(db, m)
	return name, "", nil
}
