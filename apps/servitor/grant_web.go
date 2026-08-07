// Connecting an agent to a machine.
//
// This is the first link in the chain and it was the last one built, because
// until something read the grant records a UI for writing them would have been
// a form that did nothing. Now everything downstream reads them: the provider
// decides which agents get appliance tools from this, request_capability
// refuses machines the agent is not connected to, and the exec gate resolves
// auto-run permission against it.
//
// CONNECTING AND PERMITTING ARE SEPARATE ACTS, and the form keeps them that
// way. Adding a grant with no categories says "this agent may ask about this
// machine, and may run nothing on it without approval" — which is the safe
// state, so it is the one that takes the fewest clicks. Categories are edited
// afterwards, deliberately, by someone who has thought about which ones.
//
// The alternative — a single control that connects and permits at once — makes
// the dangerous configuration as easy as the safe one, and the whole design has
// spent the day arguing that the safe answer should be the default answer.
package servitor

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
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
func (T *Servitor) handleCommandGrants(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(T.grantRows(user, udb))
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		agentID, _ := body["agent_id"].(string)
		if strings.TrimSpace(agentID) == "" {
			http.Error(w, "an agent is required", http.StatusBadRequest)
			return
		}
		appliance, _ := body["appliance_id"].(string)
		if appliance = strings.TrimSpace(appliance); appliance == "" {
			http.Error(w, "a machine is required — a grant names one agent and one machine", http.StatusBadRequest)
			return
		}
		// Connecting is not permitting: a grant written here carries no
		// categories, and the exec gate reads an empty list as "nothing runs
		// without asking". Categories are the console's own permission and are
		// not settable per agent, because nothing consults them per agent.
		g := SaveCommandGrant(udb, agentID, appliance, nil)
		// Logged because this is the moment a machine becomes reachable by
		// something that is not a person, and because the categories decide how
		// much of it runs without anyone being asked.
		Log("[servitor] agent %q connected to %s with auto-run: %s", g.AgentID, g.ApplianceID, allowsText(g.Categories))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		agent := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		appliance := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
		if agent == "" || appliance == "" {
			http.Error(w, "agent_id and appliance_id are required", http.StatusBadRequest)
			return
		}
		DeleteCommandGrant(udb, agent, appliance)
		Log("[servitor] agent %q disconnected from %s", agent, appliance)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// grantRows renders every connection with names rather than ids, since an id is
// not what anyone recognizes their own fleet by.
func (T *Servitor) grantRows(user string, udb Database) []grantRow {
	agents := map[string]string{}
	for _, t := range ListExternalTargets(RootDB, user) {
		if t.Group == "Agents" {
			agents[strings.ToLower(t.Value)] = t.Label
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
			Allows: allowsText(g.Categories), Categories: g.Categories,
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

// allowsText says what runs without asking. The empty case gets a sentence
// rather than a dash: "nothing without asking" is a decision someone made, and
// it should read like one.
func allowsText(cats []string) string {
	if len(cats) == 0 {
		return "nothing without asking"
	}
	return strings.Join(cats, ", ")
}

// agentAccessOptions lists the user's agents for the appliance form.
func agentAccessOptions(user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, t := range ListExternalTargets(RootDB, user) {
		if t.Group == "Agents" {
			out = append(out, ui.SelectOption{Value: t.Value, Label: t.Label})
		}
	}
	return out
}

// handleAccessAgents lists the user's agents for the per-appliance access
// modal. Its own endpoint because the modal is hand-written JS on the chat
// page, which has no server-rendered form to bake options into.
func (T *Servitor) handleAccessAgents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	out := []ui.SelectOption{}
	for _, t := range ListExternalTargets(RootDB, user) {
		if t.Group == "Agents" {
			out = append(out, ui.SelectOption{Value: t.Value, Label: t.Label})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
