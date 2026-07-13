package orchestrate

// Agent as a portable artifact: wires agent export/import into the unified
// gohort.bundle/v1 surface (core/artifact_pack.go). Agents already had a
// recipe (agentExport: identity-stripped record + owned sub-agents, reborn on
// import) — this exposes that recipe as the "agent" artifact type so a single
// bundle can carry agents alongside connectors and tools.
//
// WHY THIS LIVES IN orchestrate, NOT core: agents are stored per-user in
// UserDB(T.DB, owner) — the orchestrate app's DB, NOT the RootDB the registry
// hands every ArtifactType. So this type ignores the db argument and resolves
// its own per-user store from the captured app. Registered from Routes(), where
// the app instance is in hand.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// RegisterAgentArtifactType wires the "agent" type into the artifact-bundle
// registry, capturing the app so per-user agent stores resolve correctly.
func RegisterAgentArtifactType(app *OrchestrateApp) {
	if app == nil {
		return
	}
	RegisterArtifactType(&agentArtifact{app: app})
}

type agentArtifact struct{ app *OrchestrateApp }

func (*agentArtifact) ArtifactType() string { return "agent" }

// ListArtifacts enumerates every user's TOP-LEVEL agents (sub-agents ride inside
// their parent's recipe, so they aren't listed on their own). Owner is set so
// export resolves the right per-user store.
func (a *agentArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if a.app == nil || a.app.DB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(a.app.DB, u.Username)
		if udb == nil {
			continue
		}
		for _, rec := range listAgents(udb, u.Username) {
			if rec.OwnedBy != "" {
				continue // sub-agent — bundled with its parent
			}
			out = append(out, ArtifactSel{Type: "agent", Name: rec.Name, Owner: u.Username})
		}
	}
	return out
}

// ExportArtifact resolves the named top-level agent in owner's store and returns
// its recipe (record + owned sub-agents), identity stripped.
func (a *agentArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("agent export requires an owner")
	}
	udb := UserDB(a.app.DB, owner)
	if udb == nil {
		return nil, fmt.Errorf("no store for user %q", owner)
	}
	for _, rec := range listAgents(udb, owner) {
		if rec.OwnedBy == "" && rec.Name == name {
			exp, ok := buildAgentExport(udb, rec.ID, owner)
			if !ok {
				return nil, fmt.Errorf("no agent named %q for user %q", name, owner)
			}
			return json.Marshal(exp)
		}
	}
	return nil, fmt.Errorf("no agent named %q for user %q", name, owner)
}

// Dependencies folds in the exportable temp tools this agent — and its bundled
// sub-agents — name in their AllowedTools allowlist, so exporting an agent
// carries the custom tools it was built to call and, transitively (the tool
// artifact's own closure), those tools' API credentials.
//
// Only EXPLICITLY allowlisted names count. An empty AllowedTools means the
// agent draws on the default deployment-wide pool of approved tools, which is
// not an agent-specific dependency — pulling every persistent tool into the
// bundle would be wrong. Built-in registered tool names (web_search, fetch_url)
// and any name that isn't an exportable temp tool are skipped by
// IsExportableTool. db is the RootDB the temp-tool store lives in; the agent
// record itself resolves from the per-user app store, same as ExportArtifact.
func (a *agentArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" || a.app == nil || a.app.DB == nil {
		return nil
	}
	udb := UserDB(a.app.DB, owner)
	if udb == nil {
		return nil
	}
	// Resolve the top-level agent by name, then pull its whole recipe (record +
	// owned sub-agents) so sub-agent allowlists are covered too.
	var exp agentExport
	found := false
	for _, rec := range listAgents(udb, owner) {
		if rec.OwnedBy == "" && rec.Name == name {
			if e, ok := buildAgentExport(udb, rec.ID, owner); ok {
				exp, found = e, true
			}
			break
		}
	}
	if !found {
		return nil
	}
	return agentExportDeps(db, exp, owner, nil)
}

// RecipeDependencies extracts the same references straight from a recipe (the
// recipe IS an agentExport), for import preview. inBundle lets an allowlisted
// tool traveling in the same bundle count as a portable-tool reference even
// though it isn't in the store yet.
func (a *agentArtifact) RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	var exp agentExport
	if json.Unmarshal(recipe, &exp) != nil {
		return nil
	}
	return agentExportDeps(db, exp, strings.TrimSpace(owner), inBundle)
}

// agentExportDeps is the one walk behind both dependency interfaces: the
// portable temp tools the recipe (parent + bundled sub-agents) allowlists,
// plus the collections it attaches. Built-in tool names fail both tool
// probes and are skipped; the well-known deployment-knowledge collection
// exists on every install, so it is never a dependency.
func agentExportDeps(db Database, exp agentExport, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	seen := map[string]bool{}
	var out []ArtifactSel
	consider := func(names []string) {
		for _, tn := range names {
			tn = strings.TrimSpace(tn)
			if tn == "" || seen["tool\x00"+tn] {
				continue
			}
			seen["tool\x00"+tn] = true
			if IsExportableTool(db, tn, owner) || (inBundle != nil && inBundle("tool", tn)) {
				out = append(out, ArtifactSel{Type: "tool", Name: tn, Owner: owner})
			}
		}
	}
	collections := func(ids []string) {
		for _, cid := range ids {
			cid = strings.TrimSpace(cid)
			if cid == "" || cid == DeploymentKnowledgeCollectionID || seen["collection\x00"+cid] {
				continue
			}
			seen["collection\x00"+cid] = true
			out = append(out, ArtifactSel{Type: "collection", Name: cid, Owner: owner})
		}
	}
	consider(exp.AllowedTools)
	collections(exp.AttachedCollections)
	for _, s := range exp.SubAgents {
		consider(s.AllowedTools)
		collections(s.AttachedCollections)
	}
	return out
}

// ImportArtifact reconstitutes an agent recipe under owner as a NEW agent (fresh
// id, reborn sub-agents). A same-named top-level agent is skipped, never
// clobbered — consistent with connector/tool import. Unlike those, agents have
// no separate approval gate: an imported agent is usable immediately, but any
// tools its allowlist names must themselves exist/be approved to actually fire.
func (a *agentArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("agent import requires an owner")
	}
	var imp agentExport
	if err := json.Unmarshal(recipe, &imp); err != nil {
		return "", "", fmt.Errorf("invalid agent recipe: %w", err)
	}
	name := strings.TrimSpace(imp.Name)
	if name == "" {
		return "", "", Error("missing agent name")
	}
	udb := UserDB(a.app.DB, owner)
	if udb == nil {
		return name, "", fmt.Errorf("no store for user %q", owner)
	}
	for _, rec := range listAgents(udb, owner) {
		if rec.OwnedBy == "" && rec.Name == name {
			return name, "an agent with this name already exists", nil
		}
	}
	if _, _, err := importAgentRecipe(udb, imp, owner); err != nil {
		return name, "", err
	}
	return name, "", nil
}
