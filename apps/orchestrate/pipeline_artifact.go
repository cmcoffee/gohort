package orchestrate

// Pipeline as a portable artifact: wires PipelineDef into the unified
// gohort.bundle/v1 surface (core/artifact_pack.go). Pipelines already had a
// portable recipe (ExportPipeline / ImportPipeline in core/pipeline_def.go);
// this exposes it as the "pipeline" artifact type and closes the portability
// gap that recipe documented: stage agent references are normalized to agent
// NAMES on export (runtime dispatch resolves either form, but an imported
// agent is reborn under a fresh ID — only a name survives the trip), and the
// dependency walk folds in the agents the stages dispatch to plus the
// exportable temp tools named in stage tool overrides.
//
// The recipe's ID travels (same rule as collections): it's the key an agent's
// AttachedPipelines references, so preserving it is what lets an
// agent+pipeline bundle land with its wiring intact.
//
// WHY THIS LIVES IN orchestrate, NOT core: same reason as agent_artifact.go —
// pipeline defs are stored per-user in UserDB(T.DB, owner), the orchestrate
// app's DB, NOT the RootDB the registry hands every ArtifactType. So this type
// ignores the db argument for store access and resolves its own per-user store
// from the captured app. Registered from Routes(), alongside the agent type.

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// RegisterPipelineArtifactType wires the "pipeline" type into the
// artifact-bundle registry, capturing the app so per-user pipeline stores
// resolve correctly.
func RegisterPipelineArtifactType(app *OrchestrateApp) {
	if app == nil {
		return
	}
	RegisterArtifactType(&pipelineArtifact{app: app})
}

type pipelineArtifact struct{ app *OrchestrateApp }

func (*pipelineArtifact) ArtifactType() string { return "pipeline" }

// ListArtifacts enumerates every user's pipeline defs. Owner is set so export
// resolves the right per-user store.
func (p *pipelineArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if p.app == nil || p.app.DB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(p.app.DB, u.Username)
		if udb == nil {
			continue
		}
		for _, d := range ListPipelineDefs(udb, u.Username) {
			out = append(out, ArtifactSel{Type: "pipeline", Name: d.Name, Owner: u.Username})
		}
	}
	return out
}

// findPipelineForExport resolves a pipeline by ID first, then by
// case-insensitive name. ID-first matters: cross-artifact references (an
// agent's AttachedPipelines) are IDs, so the dependency closure and the
// existence probe address pipelines the same way humans' export buttons
// address them by name.
func (p *pipelineArtifact) findPipelineForExport(owner, nameOrID string) (PipelineDef, bool) {
	udb := UserDB(p.app.DB, owner)
	if udb == nil {
		return PipelineDef{}, false
	}
	if d, ok := LoadPipelineDef(udb, owner, strings.TrimSpace(nameOrID)); ok {
		return d, true
	}
	lower := strings.ToLower(strings.TrimSpace(nameOrID))
	if lower == "" {
		return PipelineDef{}, false
	}
	for _, d := range ListPipelineDefs(udb, owner) {
		if strings.ToLower(strings.TrimSpace(d.Name)) == lower {
			return d, true
		}
	}
	return PipelineDef{}, false
}

// ExportArtifact resolves the named pipeline in owner's store and returns its
// recipe: agent refs normalized to names, owner/timestamps stripped, ID kept.
func (p *pipelineArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("pipeline export requires an owner")
	}
	d, ok := p.findPipelineForExport(owner, name)
	if !ok {
		return nil, fmt.Errorf("no pipeline named %q for user %q", name, owner)
	}
	return json.Marshal(ExportPipeline(p.normalizeStageAgents(d, owner)))
}

// normalizeStageAgents rewrites each stage's agent reference to the agent's
// NAME when it currently holds an ID. Runtime dispatch resolves either form
// (findAgentByNameOrID), but only the name survives an agent traveling in the
// same bundle: an imported agent is reborn under a fresh ID. A reference that
// doesn't resolve is left untouched — best-effort, same rule as the
// dependency walk. Operates on a copied stage slice; the stored def is never
// mutated.
func (p *pipelineArtifact) normalizeStageAgents(d PipelineDef, owner string) PipelineDef {
	udb := UserDB(p.app.DB, owner)
	if udb == nil {
		return d
	}
	stages := append([]PipelineStage(nil), d.Stages...)
	for i, s := range stages {
		ref := strings.TrimSpace(s.Agent)
		if ref == "" {
			continue
		}
		if a, ok := findAgentByNameOrID(udb, owner, ref); ok && strings.TrimSpace(a.Name) != "" {
			stages[i].Agent = a.Name
		}
	}
	d.Stages = stages
	return d
}

// Dependencies folds in what the pipeline's stages reference: the agents they
// dispatch to and the exportable temp tools named in stage tool overrides —
// so exporting a pipeline carries the capabilities it was built to call and,
// transitively (each type's own closure), everything THOSE need. db is the
// RootDB the temp-tool store lives in; the pipeline itself resolves from the
// per-user app store, same as ExportArtifact.
func (p *pipelineArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" || p.app == nil || p.app.DB == nil {
		return nil
	}
	d, ok := p.findPipelineForExport(owner, name)
	if !ok {
		return nil
	}
	return p.pipelineRecipeDeps(db, d, owner, nil)
}

// RecipeDependencies extracts the same references straight from a recipe (the
// recipe IS a PipelineDef), for import preview. inBundle lets a referenced
// agent or tool traveling in the same bundle count even though it isn't in
// any store yet.
func (p *pipelineArtifact) RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	var d PipelineDef
	if json.Unmarshal(recipe, &d) != nil {
		return nil
	}
	return p.pipelineRecipeDeps(db, d, strings.TrimSpace(owner), inBundle)
}

// pipelineRecipeDeps is the one walk behind both dependency interfaces. A
// stage's agent reference resolves through the owner's store and is emitted
// as the TOP-LEVEL agent's name — a sub-agent rides inside its parent's
// recipe, so the parent is the exportable unit; a reference that doesn't
// resolve locally still counts when the agent travels in the bundle under
// preview. A stage-tools name is a tool reference when it resolves to an
// exportable temp tool or travels in the bundle — built-ins fail both and
// are skipped, same rule as the agent walk.
func (p *pipelineArtifact) pipelineRecipeDeps(db Database, d PipelineDef, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	if owner == "" || p.app == nil || p.app.DB == nil {
		return nil
	}
	udb := UserDB(p.app.DB, owner)
	seen := map[string]bool{}
	var out []ArtifactSel
	add := func(typ, name string) {
		key := typ + "\x00" + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ArtifactSel{Type: typ, Name: name, Owner: owner})
	}
	for _, s := range d.Stages {
		if ref := strings.TrimSpace(s.Agent); ref != "" {
			if a, ok := findAgentByNameOrID(udb, owner, ref); ok {
				if a.OwnedBy != "" {
					if parent, pok := loadAgent(udb, a.OwnedBy); pok {
						a = parent
					}
				}
				add("agent", strings.TrimSpace(a.Name))
			} else if inBundle != nil && inBundle("agent", ref) {
				add("agent", ref)
			}
		}
		for _, tn := range s.Tools {
			tn = strings.TrimSpace(tn)
			if IsExportableTool(db, tn, owner) || (inBundle != nil && inBundle("tool", tn)) {
				add("tool", tn)
			} else if h, ok := FindSourceHookByToolName(tn); ok {
				// A hook-backed stage tool references the source hook itself.
				if !seen["source_hook\x00"+h.Name] {
					seen["source_hook\x00"+h.Name] = true
					out = append(out, ArtifactSel{Type: "source_hook", Name: h.Name})
				}
			}
		}
	}
	return out
}

// ImportArtifact reconstitutes a pipeline recipe under owner. The traveled ID
// is preserved (ImportPipeline) — it's what an agent in the same bundle
// references via AttachedPipelines. A same-ID or same-named pipeline already
// in owner's store skips, never clobbered — consistent with the other types.
// Like agents, pipelines have no separate approval gate: the def is prompts
// plus references, and everything it calls (tools, agents) is governed at its
// own layer.
func (p *pipelineArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("pipeline import requires an owner")
	}
	var d PipelineDef
	if err := json.Unmarshal(recipe, &d); err != nil {
		return "", "", fmt.Errorf("invalid pipeline recipe: %w", err)
	}
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return "", "", Error("missing pipeline name")
	}
	udb := UserDB(p.app.DB, owner)
	if udb == nil {
		return name, "", fmt.Errorf("no store for user %q", owner)
	}
	if id := strings.TrimSpace(d.ID); id != "" {
		if _, exists := LoadPipelineDef(udb, owner, id); exists {
			return name, "a pipeline with this id already exists", nil
		}
	}
	for _, ex := range ListPipelineDefs(udb, owner) {
		if strings.EqualFold(strings.TrimSpace(ex.Name), name) {
			return name, "a pipeline with this name already exists", nil
		}
	}
	if _, err := ImportPipeline(udb, owner, d); err != nil {
		return name, "", err
	}
	return name, "", nil
}
