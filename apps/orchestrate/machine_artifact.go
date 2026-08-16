// Machine as a portable artifact: wires MachineDef into the unified
// gohort.bundle/v1 surface (core/artifact_pack.go).
//
// Machines already had a portable recipe (ExportMachine / ImportMachine)
// and their own HTTP export/import; what was missing was membership in
// the bundle registry — and that gap had a sharp edge: an agent's
// Machine pointer travels in its recipe (stripAgentIdentity keeps it),
// so an exported agent arrived pointing at a machine that was never in
// the box. The agent walked and talked and had quietly stopped being
// what its author built, with only an enterMachine breadcrumb to say so.
// With the type registered, the agent's dependency walk carries the
// machine along, the same way it already carries pipelines.
//
// Same store note as pipeline_artifact.go: machine defs live per-user in
// UserDB(T.DB, owner), so this type ignores the registry's RootDB for
// store access and resolves through the captured app.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// RegisterMachineArtifactType wires the "machine" type into the
// artifact-bundle registry.
func RegisterMachineArtifactType(app *OrchestrateApp) {
	if app == nil {
		return
	}
	RegisterArtifactType(&machineArtifact{app: app})
}

type machineArtifact struct{ app *OrchestrateApp }

func (*machineArtifact) ArtifactType() string { return "machine" }

// ListArtifacts enumerates every user's machines, Owner set so export
// resolves the right per-user store.
func (m *machineArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if m.app == nil || m.app.DB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(m.app.DB, u.Username)
		if udb == nil {
			continue
		}
		for _, d := range ListMachineDefs(udb, u.Username) {
			out = append(out, ArtifactSel{Type: "machine", Name: d.Name, Owner: u.Username})
		}
	}
	return out
}

// findMachineForExport resolves by ID first, then case-insensitive name.
// ID-first matters: an agent's Machine reference is an ID, so the
// dependency closure addresses machines the way agents do, while a
// person's export button addresses them by name.
func (m *machineArtifact) findMachineForExport(owner, nameOrID string) (MachineDef, bool) {
	udb := UserDB(m.app.DB, owner)
	if udb == nil {
		return MachineDef{}, false
	}
	if d, ok := LoadMachineDef(udb, owner, strings.TrimSpace(nameOrID)); ok {
		return d, true
	}
	lower := strings.ToLower(strings.TrimSpace(nameOrID))
	if lower == "" {
		return MachineDef{}, false
	}
	for _, d := range ListMachineDefs(udb, owner) {
		if strings.ToLower(strings.TrimSpace(d.Name)) == lower {
			return d, true
		}
	}
	return MachineDef{}, false
}

// ExportArtifact returns the recipe: delegate references normalized to
// agent NAMES (an imported agent is reborn under a fresh ID — only a
// name survives the trip), identity stripped, ID kept for the wiring.
func (m *machineArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, Error("machine export requires an owner")
	}
	d, ok := m.findMachineForExport(owner, name)
	if !ok {
		return nil, fmt.Errorf("no machine named %q for user %q", name, owner)
	}
	return json.Marshal(ExportMachine(m.normalizeDelegates(d, owner)))
}

// normalizeDelegates rewrites each phase's delegate reference to the
// agent's NAME when it holds an ID — same rule and reason as pipeline
// stage agents. Copied slice; the stored def is never mutated.
func (m *machineArtifact) normalizeDelegates(d MachineDef, owner string) MachineDef {
	udb := UserDB(m.app.DB, owner)
	if udb == nil {
		return d
	}
	phases := append([]MachinePhase(nil), d.Phases...)
	for i, p := range phases {
		ref := strings.TrimSpace(p.Agent)
		if ref == "" {
			continue
		}
		if a, ok := findAgentByNameOrID(udb, owner, ref); ok && strings.TrimSpace(a.Name) != "" {
			phases[i].Agent = a.Name
		}
	}
	d.Phases = phases
	return d
}

// Dependencies folds in what the machine's steps reference: the agents
// steps delegate to and the exportable tools named in step narrowing.
func (m *machineArtifact) Dependencies(db Database, name, owner string) []ArtifactSel {
	owner = strings.TrimSpace(owner)
	if owner == "" || m.app == nil || m.app.DB == nil {
		return nil
	}
	d, ok := m.findMachineForExport(owner, name)
	if !ok {
		return nil
	}
	return m.machineRecipeDeps(db, d, owner, nil)
}

// RecipeDependencies extracts the same references straight from a
// recipe, for import preview.
func (m *machineArtifact) RecipeDependencies(db Database, recipe json.RawMessage, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	var d MachineDef
	if json.Unmarshal(recipe, &d) != nil {
		return nil
	}
	return m.machineRecipeDeps(db, d, strings.TrimSpace(owner), inBundle)
}

// machineRecipeDeps is the one walk behind both dependency interfaces —
// the same shape as pipelineRecipeDeps, over phases instead of stages.
func (m *machineArtifact) machineRecipeDeps(db Database, d MachineDef, owner string, inBundle func(typ, name string) bool) []ArtifactSel {
	if owner == "" || m.app == nil || m.app.DB == nil {
		return nil
	}
	udb := UserDB(m.app.DB, owner)
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
	for _, p := range d.Phases {
		if ref := strings.TrimSpace(p.Agent); ref != "" {
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
		for _, tn := range p.Tools {
			tn = strings.TrimSpace(tn)
			if IsExportableTool(db, tn, owner) || (inBundle != nil && inBundle("tool", tn)) {
				add("tool", tn)
			} else if h, ok := FindSourceHookByToolName(tn); ok {
				if !seen["source_hook\x00"+h.Name] {
					seen["source_hook\x00"+h.Name] = true
					out = append(out, ArtifactSel{Type: "source_hook", Name: h.Name})
				}
			}
		}
	}
	return out
}

// ImportArtifact reconstitutes a machine recipe under owner. Skip rules
// match pipelines: a same-ID machine already present serves the agent
// reference the traveled ID exists for, and a same-named one is never
// clobbered. ImportMachine would remint a colliding ID into a copy —
// right for a person importing twice on purpose, wrong for a bundle,
// where the copy would strand the agent pointer the skip preserves.
func (m *machineArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", "", Error("machine import requires an owner")
	}
	var d MachineDef
	if err := json.Unmarshal(recipe, &d); err != nil {
		return "", "", fmt.Errorf("invalid machine recipe: %w", err)
	}
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return "", "", Error("machine recipe has no name")
	}
	udb := UserDB(m.app.DB, owner)
	if udb == nil {
		return "", "", Error("no store for owner")
	}
	if d.ID != "" {
		if _, exists := LoadMachineDef(udb, "", d.ID); exists {
			return name, "a machine with this id already exists", nil
		}
	}
	for _, existing := range ListMachineDefs(udb, owner) {
		if strings.EqualFold(strings.TrimSpace(existing.Name), name) {
			return name, "a machine named "+strconv.Quote(name)+" already exists", nil
		}
	}
	saved, err := ImportMachine(udb, owner, d)
	if err != nil {
		return name, "", err
	}
	return saved.Name, "", nil
}
