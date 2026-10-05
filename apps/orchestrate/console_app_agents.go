package orchestrate

// Fleet > App agents: the agents apps bring with them, where they can be
// seen, changed and put back.
//
// An app agent (Scribe's Guide Author, Servitor's investigator) runs behind
// its app's own chat, so most of them are Hidden: the chat picker leaves them
// out, rightly, since they cannot run without the tools their app hands them
// each turn. That also left them with no editor anyone could find. Their
// budgets, reasoning and the rest ran on whatever the app registered, and a
// setting changed on one somehow had no way back.
//
// Each person has their own copy of each app agent, layered over the app's
// definition (agent_overlay.go): what they change is theirs, everything else
// follows the app. Reset removes their copy, so the agent reads as the app
// registered it again.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// appAgentRow is one app agent on the Fleet page. A struct, not a map, so
// the card shows its fields in this order.
type appAgentRow struct {
	Name       string `json:"Agent"`
	Status     string `json:"Status,omitempty"`
	Changed    string `json:"Changed,omitempty"`
	About      string `json:"About,omitempty"`
	ID         string `json:"_id"`
	Customized bool   `json:"_customized,omitempty"`
	Section    string `json:"_section"`
}

// appAgentRows lists every registered app agent for user, grouped by the app
// that owns it, hidden ones included.
func appAgentRows(udb Database) []appAgentRow {
	specs := appagents.AppAgents()
	sort.SliceStable(specs, func(i, j int) bool {
		if specs[i].OwningApp != specs[j].OwningApp {
			return specs[i].OwningApp < specs[j].OwningApp
		}
		return specs[i].Name < specs[j].Name
	})
	rows := []appAgentRow{}
	for _, sp := range specs {
		row := appAgentRow{Name: sp.Name, About: sp.Description, ID: sp.ID, Section: chFirst(sp.OwningApp, "App agents")}
		if rec, ok := loadAgent(udb, sp.ID); ok && rec.Name != "" {
			row.Name = rec.Name
		}
		if changed := appAgentChanges(udb, sp.ID); changed != nil {
			row.Customized, row.Status = true, "customized"
			row.Changed = "changed: " + strings.Join(changed, ", ")
			if len(changed) == 0 {
				row.Changed = ""
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// appAgentChanges is what the user has changed on an app agent, by field
// name, or nil when they have no copy of it. A copy with nothing listed is
// still a copy (a tool approval writes one), so it is an empty, non-nil list.
func appAgentChanges(udb Database, id string) []string {
	var stored AgentRecord
	if udb == nil || !udb.Get(agentsTable, id, &stored) {
		return nil
	}
	fields := stored.OverriddenFields
	if stored.OverlayRev == 0 {
		if seed, ok := seedAgentByID(id); ok {
			fields = agentOverrides(seed, stored, frameworkOwnedSeedFields(id))
		}
	}
	out := []string{}
	for _, f := range fields {
		out = append(out, strings.ReplaceAll(f, "_", " "))
	}
	return out
}

func (T *OrchestrateApp) handleConsoleAppAgents(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	writeJSON(w, appAgentRows(udb))
}

// resetAppAgent removes the user's copy of an app agent, so it reads as its
// app registered it. Its memory and conversations are kept: a reset is about
// how the agent is set up, not what it has learned or been told. (Reverting a
// framework seed drops those too, because there the persona the memory grew
// under is what is being thrown away; an app agent's persona is the app's
// and never changed.)
func resetAppAgent(udb Database, id string) error {
	if _, isApp := appagents.AppAgentByID(id); !isApp {
		return fmt.Errorf("%q is not an app agent", id)
	}
	var stored AgentRecord
	if udb == nil || !udb.Get(agentsTable, id, &stored) {
		return fmt.Errorf("it is already at its defaults")
	}
	if stored.Locked {
		return fmt.Errorf("it is locked: unlock it in its editor first")
	}
	udb.Unset(agentsTable, id)
	return nil
}

func (T *OrchestrateApp) handleConsoleAppAgentReset(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if err := resetAppAgent(udb, id); err != nil {
		http.Error(w, "Not reset: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
