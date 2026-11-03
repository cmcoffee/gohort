// Which of the user's agents load a tool they added from the catalog.
//
// Adding a shared tool puts it in the user's pool, for all their agents. The
// Agents dialog on the added row narrows it to some of them, the same choice
// the user makes for a tool of their own: All my agents, or Only these
// agents. The owner's own agent list never travels with a tool, so this is
// the recipient's to decide (core.SetAdoptedToolAgents).

package extensions

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	adoptedForAll    = "all"
	adoptedForAgents = "agents"
)

// handleAdoptedAgents answers GET ?name= with the dialog's record {who,
// agents}, and GET ?name=&options=1 with the agents to tick.
func (T *Extensions) handleAdoptedAgents(c *jsonapi.Call) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	c.W.Header().Set("Cache-Control", "no-store")
	if c.R.URL.Query().Get("options") != "" {
		return adoptedAgentOptions(c.User, name), nil
	}
	agents, ok := adoptedAgents(c.User, name)
	if !ok {
		return nil, jsonapi.NotFound("you have not added a tool called " + name)
	}
	who := adoptedForAll
	if len(agents) > 0 {
		who = adoptedForAgents
	}
	return map[string]any{"who": who, "agents": nonNilList(agents)}, nil
}

// handleAdoptedAgentsSet saves the dialog: {who: "all"} for every agent, or
// {who: "agents", agents: [...]} for only those, at least one.
func (T *Extensions) handleAdoptedAgentsSet(c *jsonapi.Call, body *struct {
	Who    string   `json:"who"`
	Agents []string `json:"agents"`
}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if body == nil {
		return nil, jsonapi.BadRequest("bad request")
	}
	var agents []string
	switch body.Who {
	case adoptedForAll:
	case adoptedForAgents:
		known := map[string]bool{}
		for _, o := range adoptedAgentOptions(c.User, name) {
			known[o.Value] = true
		}
		for _, id := range body.Agents {
			if known[id] {
				agents = append(agents, id)
			}
		}
		if len(agents) == 0 {
			return nil, jsonapi.BadRequest("tick at least one agent, or choose All my agents")
		}
	default:
		return nil, jsonapi.BadRequest("choose All my agents or Only these agents")
	}
	if err := SetAdoptedToolAgents(AuthDB(), c.User, name, agents); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// adoptedAgents is which of the user's agents load a tool they added (none
// listed for all of them), read from what their agents load.
func adoptedAgents(user, name string) ([]string, bool) {
	for _, p := range AdoptedToolsFor(AuthDB(), user) {
		if p.Tool.Name == name {
			return p.ScopeAgents, true
		}
	}
	return nil, false
}

// adoptedAgentOptions are the user's agents a tool can be put on: the same
// list a tool of their own offers, a sub-agent shown under its parent.
func adoptedAgentOptions(user, name string) []ui.SelectOption {
	out := []ui.SelectOption{}
	prov, ok := ScopeProviderFor("tool")
	if !ok {
		return out
	}
	st, _ := prov.State(AuthDB(), user, name)
	names := map[string]string{}
	for _, a := range st.Agents {
		names[a.ID] = a.Name
	}
	seen := map[string]bool{}
	for _, a := range st.Agents {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		label := a.Name
		if p := names[a.ParentID]; p != "" {
			label = p + " ▸ " + a.Name
		}
		out = append(out, ui.SelectOption{Value: a.ID, Label: label})
	}
	return out
}

// adoptedAgentLabels is the user's agents by ID, read once per listing.
func adoptedAgentLabels(user string) map[string]string {
	labels := map[string]string{}
	for _, o := range adoptedAgentOptions(user, "") {
		labels[o.Value] = o.Label
	}
	return labels
}

// adoptedAgentsSummary is the row's Agents column: blank for all of them,
// otherwise the agents named.
func adoptedAgentsSummary(labels map[string]string, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	var out []string
	for _, id := range ids {
		if l := labels[id]; l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "None (its agents were deleted)"
	}
	return strings.Join(out, ", ")
}

// adoptedAgentsDialog is the Agents row action on an added catalog row.
func adoptedAgentsDialog() ui.RowAction {
	return ui.ModalActionIf("Agents", "adopted", "", ui.FormPanel{
		Source:      "api/global-tools/agents?name={name}",
		PostURL:     "api/global-tools/agents?name={name}",
		SubmitLabel: "Save",
		Fields: []ui.FormField{
			{Field: "who", Type: "select", Label: "Who can use it", Options: []ui.SelectOption{
				{Value: adoptedForAll, Label: "All my agents"},
				{Value: adoptedForAgents, Label: "Only these agents"},
			}, Help: "All my agents puts it in your pool, where an agent with its own tool list still has to name it. Only these agents gives it to the agents ticked."},
			{Field: "agents", Type: "checklist", Label: "Agents", ShowWhen: "who:agents",
				OptionsSource: "api/global-tools/agents?name={name}&options=1",
				Placeholder:   "(you have no agents yet)"},
		},
		Invalidate: []string{"api/global-tools"},
	})
}
