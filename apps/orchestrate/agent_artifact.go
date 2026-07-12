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
