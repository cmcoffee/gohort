// Live options for an intake form's select (IntakeField.OptionsFrom).
//
// A starting-point form that asks "Which one?" has to list what the person
// actually has, which no static option list can. The form names a source here
// and the answers so far; this answers with {value, label} pairs, scoped to the
// signed-in user.

package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

// intakeOption is one choice for a select.
type intakeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// handleIntakeOptions serves GET /api/intake/options?source=<name>&...
// Sources:
//
//	authoring&kind=agent|tool|app|pipeline|machine: the user's own things of
//	that kind, the targets Builder can change or fix.
func (T *OrchestrateApp) handleIntakeOptions(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var opts []intakeOption
	switch r.URL.Query().Get("source") {
	case "authoring":
		opts = authoringTargets(udb, user, r.URL.Query().Get("kind"))
	default:
		http.Error(w, "unknown option source", http.StatusBadRequest)
		return
	}
	if opts == nil {
		opts = []intakeOption{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(opts)
}

// authoringTargets lists what the user owns of one kind, for "Which one?".
// Agents carry their id in the value, since two can share a name; the rest
// are named uniquely. Hidden fleet members (Builder itself, retired seeds,
// app templates) are not offered: they are not the user's to change.
func authoringTargets(udb Database, user, kind string) []intakeOption {
	var out []intakeOption
	add := func(value, label string) {
		if strings.TrimSpace(value) != "" {
			out = append(out, intakeOption{Value: value, Label: label})
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "agent":
		for _, a := range listAgents(udb, user) {
			// Builder improves other agents, and a hidden agent is a template
			// or internal seat rather than something the user runs.
			// An app agent's prompt is its app's, so it is not Builder's to
			// improve: its settings are changed under Fleet > App agents.
			if fleetHidden(a.ID) || isBuilderAgent(a.ID) || a.Hidden || isAppAgent(a.ID) {
				continue
			}
			add(a.Name+" (id: "+a.ID+")", a.Name)
		}
	case "tool":
		for _, p := range LoadPersistentTempTools(RootDB, user) {
			if p.Tool.Mode == TempToolModePipeline {
				continue // listed under pipelines, where a person looks for it
			}
			add(p.Tool.Name, p.Tool.Name)
		}
	case "pipeline":
		for _, p := range ListPipelineDefs(udb, user) {
			add(p.Name, p.Name)
		}
		for _, p := range LoadPersistentTempTools(RootDB, user) {
			if p.Tool.Mode == TempToolModePipeline {
				add(p.Tool.Name, p.Tool.Name+" (pipeline tool)")
			}
		}
	case "app":
		for _, a := range ListAppSpecs(user) {
			add(a.Slug, chFirst(a.Name, a.Slug))
		}
	case "machine":
		for _, m := range ListMachineDefs(udb, user) {
			add(m.Name, m.Name)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}
