// Connecting an agent to a machine, from the machine's side.
//
// An agent reaches a system by having it among its Sources, and this screen
// edits the same switch the agent's own page does. Connecting writes nothing
// else: the agent runs under its owner's own auto-run settings, as the owner
// does at the console. Its questions are read-only with anything destructive
// refused, and a command it needs becomes a capability the owner approves
// while reading it.
//
// A command grant (one agent, one system, its own risk categories) is an
// override of the owner's settings. None is written by default; the one
// connecting used to write always said "nothing without asking", which is not
// what governed the agent, so those were removed (dropEmptyGrants).
package servitor

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// grantRow is one connection as the table reads it.
type grantRow struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	Agent       string `json:"agent"`
	ApplianceID string `json:"appliance_id"`
	Appliance   string `json:"appliance"`
	// Allows is the human reading of the categories, or the sentence that says
	// there are none. "—" would leave an operator guessing whether the grant is
	// broken or deliberately empty.
	Allows string `json:"allows"`
	// Categories is the raw list, kept for callers that want it whole.
	Categories []string `json:"categories"`
}

// handleCommandGrants lists connections; POST creates or updates one.
func (T *Servitor) handleCommandGrants(c *jsonapi.Call) (any, error) {
	return T.grantRows(c.User, c.UDB), nil
}

// handleCommandGrantAdd connects an agent to a machine.
func (T *Servitor) handleCommandGrantAdd(c *jsonapi.Call, body map[string]any) (any, error) {
	user, udb := c.User, c.UDB
	agentID, _ := body["agent_id"].(string)
	if strings.TrimSpace(agentID) == "" {
		return nil, jsonapi.BadRequest("an agent is required")
	}
	appliance, _ := body["appliance_id"].(string)
	if appliance = strings.TrimSpace(appliance); appliance == "" {
		return nil, jsonapi.BadRequest("a machine is required: a grant names one agent and one machine")
	}
	// The connection is the agent's Source, the switch its own page
	// shows and removes. Without the agent runtime (no Sources hook) the
	// grant record is the connection, as it was before Sources.
	if SetAgentSource == nil {
		SaveCommandGrant(udb, agentID, appliance, nil)
	} else if err := SetAgentSource(user, normalizeAgentID(agentID), ReferenceSelection{Kind: "system", ItemID: appliance}, true); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "could not add it to the agent's Sources: "+err.Error())
	}
	// Logged because this is the moment a machine becomes reachable by
	// something that is not a person.
	Log("[servitor] agent %q connected to %s, under %s's own auto-run settings", normalizeAgentID(agentID), appliance, user)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleCommandGrantRemove disconnects an agent from a machine.
func (T *Servitor) handleCommandGrantRemove(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	agent := strings.TrimSpace(q.Get("agent_id"))
	appliance := strings.TrimSpace(q.Get("appliance_id"))
	if agent == "" || appliance == "" {
		return nil, jsonapi.BadRequest("agent_id and appliance_id are required")
	}
	DeleteCommandGrant(c.UDB, agent, appliance)
	if SetAgentSource != nil {
		if err := SetAgentSource(c.User, normalizeAgentID(agent), ReferenceSelection{Kind: "system", ItemID: appliance}, false); err != nil {
			Log("[servitor] disconnecting %q from %s: could not remove it from the agent's Sources: %v", agent, appliance, err)
		}
	}
	Log("[servitor] agent %q disconnected from %s", agent, appliance)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// grantRows renders every connection with names rather than ids, since an id is
// not what anyone recognizes their own fleet by.
func (T *Servitor) grantRows(user string, udb Database) []grantRow {
	agents := map[string]string{}
	for _, t := range ListExternalTargets(RootDB, user) {
		if t.Group == "Agents" {
			agents[strings.ToLower(normalizeAgentID(t.Value))] = t.Label
		}
	}
	appliances := map[string]string{}
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, k, &a) {
			appliances[strings.ToLower(a.ID)] = applianceLabel(a.Name, a.ID)
		}
	}
	all := ListCommandGrants(udb)
	// With Sources as the door, a grant is access only while the agent still
	// has the system among its Sources, and an agent that has it but has not
	// used it yet holds no grant: list what the Sources say.
	if AgentSourcesFor != nil {
		kept := all[:0:0]
		has := map[string]bool{}
		for _, g := range all {
			if AgentHasSource(user, g.AgentID, "system", g.ApplianceID) {
				kept = append(kept, g)
				has[strings.ToLower(g.AgentID)+"/"+strings.ToLower(g.ApplianceID)] = true
			}
		}
		for id := range agents {
			for _, s := range AgentSourcesFor(user, id) {
				if s.Kind == "system" && !has[id+"/"+strings.ToLower(s.ItemID)] {
					kept = append(kept, CommandGrant{AgentID: id, ApplianceID: s.ItemID, ownSettings: true})
				}
			}
		}
		all = kept
	}
	rows := make([]grantRow, 0, len(all))
	for _, g := range all {
		agent := agents[strings.ToLower(g.AgentID)]
		if agent == "" {
			// The agent was deleted and the grant outlived it. Shown, not
			// hidden: a connection to nothing is what an owner should be able
			// to find and remove.
			agent = g.AgentID + " (removed)"
		}
		appliance := appliances[strings.ToLower(g.ApplianceID)]
		if appliance == "" {
			appliance = g.ApplianceID + " (removed)"
		}
		rows = append(rows, grantRow{
			ID: g.AgentID + "/" + g.ApplianceID, AgentID: g.AgentID, Agent: agent,
			ApplianceID: g.ApplianceID, Appliance: appliance,
			Allows: grantAllowsText(g), Categories: g.Categories,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
		}
		return rows[i].Appliance < rows[j].Appliance
	})
	return rows
}

// grantAllowsText is allowsText for a row that may have no grant behind it,
// which is the ordinary case: the owner's own settings apply.
func grantAllowsText(g CommandGrant) string {
	if g.ownSettings {
		return "same as your own auto-run settings"
	}
	return allowsText(g.Categories)
}

// allowsText says what runs without asking. The empty case gets a sentence
// rather than a dash: "nothing without asking" is a decision someone made, and
// it should read like one.
func allowsText(cats []string) string {
	if len(cats) == 0 {
		return "nothing without asking"
	}
	return strings.Join(cats, ", ")
}

// agentAccessOptions lists the user's agents for the appliance form. Values
// are the BARE agent ids — the picker's "agent:" wrapper is stripped, because
// what a grant stores has to be what a run dispatches with.
func agentAccessOptions(user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, t := range ListExternalTargets(RootDB, user) {
		if t.Group == "Agents" {
			out = append(out, ui.SelectOption{Value: normalizeAgentID(t.Value), Label: t.Label})
		}
	}
	return out
}

// handleAccessAgents lists the user's agents for the per-appliance access
// modal. Its own endpoint because the modal is hand-written JS on the chat
// page, which has no server-rendered form to bake options into.
func (T *Servitor) handleAccessAgents(c *jsonapi.Call) (any, error) {
	out := []ui.SelectOption{}
	for _, t := range ListExternalTargets(RootDB, c.User) {
		if t.Group == "Agents" {
			out = append(out, ui.SelectOption{Value: normalizeAgentID(t.Value), Label: t.Label})
		}
	}
	return out, nil
}
