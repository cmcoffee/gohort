package orchestrate

// What uses an agent: the apps bound to it, the pipelines with a stage it
// runs, the machines with a phase it runs.
//
// Derived from those references every time it is asked, never stored on the
// agent. An app already records which agent it asks (AppSpec.AgentID), a stage
// and a phase name theirs; a "belongs to an app" flag on the agent would be a
// second copy of that, and the first time the two disagreed the agent would
// claim a dependent that had moved on, or miss one that had not.
//
// Read by the agent's page (Used by), its delete confirmation (what stops
// working), Builder's delete_agent (named in the owner's approval), and the
// note that offers to delete an agent an app no longer uses.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// agentUse is one thing that runs through the agent.
type agentUse struct {
	Kind string `json:"kind"` // app | pipeline | machine
	Name string `json:"name"`
}

// agentRefMatches reports whether a reference (an id, or a name as imported
// bundles carry it) names agent a.
func agentRefMatches(ref string, a AgentRecord) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	return ref == a.ID || (strings.TrimSpace(a.Name) != "" && strings.EqualFold(ref, strings.TrimSpace(a.Name)))
}

// agentUsedBy lists what of the owner's runs through agent a.
func agentUsedBy(udb Database, owner string, a AgentRecord) []agentUse {
	var out []agentUse
	for _, spec := range ListAppSpecs(owner) {
		if agentRefMatches(spec.AgentID, a) {
			out = append(out, agentUse{Kind: "app", Name: chFirst(spec.Name, spec.Slug)})
		}
	}
	if udb != nil {
		for _, d := range ListPipelineDefs(udb, owner) {
			for _, st := range d.Stages {
				if agentRefMatches(st.Agent, a) {
					out = append(out, agentUse{Kind: "pipeline", Name: chFirst(d.Name, d.ID)})
					break
				}
			}
		}
		for _, d := range ListMachineDefs(udb, owner) {
			for _, p := range d.Phases {
				if agentRefMatches(p.Agent, a) {
					out = append(out, agentUse{Kind: "machine", Name: chFirst(d.Name, d.ID)})
					break
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// agentUsesText is the list in one line: "Swing Screener (app), Daily brief
// (pipeline)".
func agentUsesText(uses []agentUse) string {
	parts := make([]string, 0, len(uses))
	for _, u := range uses {
		parts = append(parts, u.Name+" ("+u.Kind+")")
	}
	return strings.Join(parts, ", ")
}

// usedByKind is the names of one kind, comma-joined, for a display pair.
func usedByKind(uses []agentUse, kind string) string {
	var names []string
	for _, u := range uses {
		if u.Kind == kind {
			names = append(names, u.Name)
		}
	}
	return strings.Join(names, ", ")
}

// handleAgentUsedBy is GET /api/agents/<id>/used-by: {uses, apps, pipelines,
// machines, text}, the agent page's Used by section.
func (T *OrchestrateApp) handleAgentUsedBy(w http.ResponseWriter, udb Database, user, id string) {
	a, ok := loadAgent(udb, id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	uses := agentUsedBy(udb, user, a)
	if uses == nil {
		uses = []agentUse{}
	}
	jsonapi.Write(w, map[string]any{
		"uses":      uses,
		"apps":      usedByKind(uses, "app"),
		"pipelines": usedByKind(uses, "pipeline"),
		"machines":  usedByKind(uses, "machine"),
		"text":      agentUsesText(uses),
	})
}
