// Live options for an intake form's select (IntakeField.OptionsFrom).
//
// A starting-point form that asks "Which one?" has to list what the person
// actually has, which no static option list can. The form names a source here
// and the answers so far; this answers with {value, label} pairs, scoped to the
// signed-in user.

package orchestrate

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
//	build_on&kind=...: what the thing being built could use, for "What should
//	it build on?" (buildOnOptions).
func (T *OrchestrateApp) handleIntakeOptions(c *jsonapi.Call) (any, error) {
	r := c.R
	user, udb := c.User, c.UDB
	var opts []intakeOption
	switch r.URL.Query().Get("source") {
	case "authoring":
		opts = authoringTargets(udb, user, r.URL.Query().Get("kind"))
	case "build_on":
		opts = buildOnOptions(udb, user, r.URL.Query().Get("kind"))
	default:
		return nil, jsonapi.BadRequest("unknown option source")
	}
	if opts == nil {
		opts = []intakeOption{}
	}
	return opts, nil
}

// PublicHandleIntakeOptions serves the same options on the restricted chat
// (/agents/<slug>/api/intake/options), whose intake form fetches them
// relative to its own page. Scoped to the signed-in user as here: the lists
// are the viewer's own things.
func (T *OrchestrateApp) PublicHandleIntakeOptions(w http.ResponseWriter, r *http.Request) {
	jsonapi.Get(T.DB, T.handleIntakeOptions)(w, r)
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
	case "skill":
		// Their own, and the ones they published, which are still theirs.
		for _, s := range append(LoadSkills(nil, user), PublishedSkillsBy(nil, user)...) {
			add(s.Name, s.Name)
		}
	case "api":
		// The APIs they set up, as Extensions lists them. The key is theirs
		// to enter; Builder drafts and checks the rest.
		for _, c := range Secure().ListUser(user) {
			add(c.Name, c.Name)
		}
	case "connection":
		// A connection is a bridge: a watch monitor that calls an API on a
		// schedule and wakes an agent when the response changes (bridge_tools.go).
		for _, m := range ListEventMonitors(RootDB, user) {
			if m.Kind == EventKindWatch && strings.HasPrefix(m.ToolName, bridgeCredToolPrefix) {
				add(m.Name, m.Name+" ("+strings.TrimPrefix(m.ToolName, bridgeCredToolPrefix)+")")
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out
}

// buildOnOptions lists what a new or changed thing of kind could be built on,
// so the person names it in Builder's intake instead of Builder searching for
// it or building its own: file stores (with their commands), APIs, MCP servers
// and the other attachable sources for an app, agent or tool; the user's tools
// and agents where those can call them.
// A value reads as what it is ("file store Intake (intake)"), since the ticked
// values reach Builder as text, and carries no comma, which the form uses to
// join them.
func buildOnOptions(udb Database, user, kind string) []intakeOption {
	kind = strings.ToLower(strings.TrimSpace(kind))
	want := func(kinds ...string) bool {
		for _, k := range kinds {
			if k == kind {
				return true
			}
		}
		return false
	}
	noComma := func(s string) string { return strings.ReplaceAll(s, ",", " ") }
	var out []intakeOption
	if want("app", "agent", "tool", "pipeline", "machine") {
		if cat, ok := appstores.Provider.(appstores.Catalog); ok && appstores.Provider != nil {
			for _, st := range cat.Stores(user, "") {
				label := "File store: " + st.Name
				var cmds []string
				for _, c := range st.Commands {
					cmds = append(cmds, chFirst(c.Label, c.Name))
				}
				if len(cmds) > 0 {
					label += " (commands: " + strings.Join(cmds, ", ") + ")"
				}
				out = append(out, intakeOption{Value: noComma("file store " + st.Name + " (" + st.Store + ")"), Label: label})
			}
		}
		for _, c := range Secure().ListUser(user) {
			out = append(out, intakeOption{Value: noComma("API " + c.Name), Label: "API: " + c.Name})
		}
		// MCP servers whose tools are offered to agents: a thing built on one
		// calls its tools.
		for _, m := range MCP().List() {
			if m.Enabled && m.ExposeTools {
				out = append(out, intakeOption{Value: noComma("MCP server " + m.Name), Label: "MCP server: " + m.Name})
			}
		}
		// The other sources an agent can attach (systems, document spaces, an
		// MCP server exposed as a source). File stores are listed above with
		// their commands, and agents below with the other agents.
		for _, g := range ReferenceGroups(user) {
			if g.Kind == "files" || g.Kind == AgentReferenceKind {
				continue
			}
			for _, it := range g.Items {
				out = append(out, intakeOption{
					Value: noComma("source " + it.Name + " (" + g.Kind + ":" + it.ID + ")"),
					Label: g.Label + ": " + it.Name,
				})
			}
		}
	}
	if want("app", "agent", "pipeline", "machine") {
		var tools []intakeOption
		for _, p := range LoadPersistentTempTools(RootDB, user) {
			tools = append(tools, intakeOption{Value: noComma("tool " + p.Tool.Name), Label: "Tool: " + p.Tool.Name})
		}
		sort.SliceStable(tools, func(i, j int) bool { return strings.ToLower(tools[i].Label) < strings.ToLower(tools[j].Label) })
		out = append(out, tools...)
		var agents []intakeOption
		for _, a := range listAgents(udb, user) {
			if fleetHidden(a.ID) || isBuilderAgent(a.ID) || a.Hidden || isAppAgent(a.ID) {
				continue
			}
			agents = append(agents, intakeOption{Value: noComma("agent " + a.Name + " (id: " + a.ID + ")"), Label: "Agent: " + a.Name})
		}
		sort.SliceStable(agents, func(i, j int) bool { return strings.ToLower(agents[i].Label) < strings.ToLower(agents[j].Label) })
		out = append(out, agents...)
	}
	return out
}
