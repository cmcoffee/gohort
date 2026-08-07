// Reading a proposed capability and deciding on it.
//
// This is the only place the loop touches a person, and the whole design leans
// on it: the model never authors a command that runs, because someone read this
// row first. So the COMMAND is a column, not something behind an expander. An
// approval surface that shows a name and a risk badge and hides the command is
// an approval surface that gets clicked through.
//
// Approve and Revoke are conditional on the row's own state rather than always
// present, because a button that does nothing on half the rows teaches people
// to stop reading which rows it applies to.
package servitor

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// applianceToolRow is one proposal as the table reads it. Flattened on purpose:
// the framework's templating addresses record fields by name, so appliance_id
// and name have to be present as plain fields for the row actions to build
// their URLs.
type applianceToolRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ApplianceID string `json:"appliance_id"`
	Appliance   string `json:"appliance"`
	Command     string `json:"command"`
	Risk        string `json:"risk"`
	RequestedBy string `json:"requested_by"`
	Status      string `json:"status"`
	// Approved is the row's state as a boolean, because RowAction.OnlyIf and
	// HideIf test truthiness rather than comparing strings. ONE field for one
	// fact: a separate "pending" flag would be a second copy of the same truth,
	// free to disagree with this one.
	Approved bool   `json:"approved"`
	Created  string `json:"created"`
}

// handleApplianceTools lists every proposal, pending first — the ones needing a
// decision are the reason anyone opens this.
func (T *Servitor) handleApplianceTools(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	names := map[string]string{}
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, k, &a) {
			names[strings.ToLower(a.ID)] = applianceLabel(a.Name, a.ID)
		}
	}
	all := ListApplianceTools(udb, "")
	rows := make([]applianceToolRow, 0, len(all))
	for _, t := range all {
		label := names[strings.ToLower(t.ApplianceID)]
		if label == "" {
			// The machine was deleted and the tool outlived it. Shown rather
			// than hidden: an orphaned capability is exactly the thing an owner
			// should be able to find and remove.
			label = t.ApplianceID + " (removed)"
		}
		status := "pending"
		if t.Approved {
			status = "approved"
		}
		by := t.MintedBy
		if strings.TrimSpace(by) == "" {
			by = "you"
		}
		rows = append(rows, applianceToolRow{
			ID: t.ApplianceID + "/" + t.Name, Name: t.Name,
			ApplianceID: t.ApplianceID, Appliance: label,
			Command: t.Template, Risk: string(t.Risk), RequestedBy: by,
			Status: status, Approved: t.Approved,
			Created: t.Created.Format("2006-01-02 15:04"),
		})
	}
	// Pending first, then by machine and name, so the decisions are at the top
	// and the rest reads as an inventory.
	sortRows(rows)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func sortRows(rows []applianceToolRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rowLess(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func rowLess(a, b applianceToolRow) bool {
	if a.Approved != b.Approved {
		return !a.Approved // undecided first: those are why anyone opens this
	}
	if a.Appliance != b.Appliance {
		return a.Appliance < b.Appliance
	}
	return a.Name < b.Name
}

// handleApplianceTool is the decision endpoint: POST sets approval, DELETE
// removes the proposal entirely.
func (T *Servitor) handleApplianceTool(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	appliance := strings.TrimSpace(r.URL.Query().Get("appliance"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if appliance == "" || name == "" {
		http.Error(w, "appliance and name are required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		approved := r.URL.Query().Get("approved") == "1"
		if !ApproveApplianceTool(udb, appliance, name, approved) {
			http.Error(w, "no such capability", http.StatusNotFound)
			return
		}
		// Logged either way. An approval is the moment a machine becomes
		// reachable by something that is not a person, and a revocation is the
		// moment someone decided it should not be — both are worth being able
		// to find later.
		Log("[servitor] capability %q on %s set approved=%v by the owner", name, appliance, approved)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		DeleteApplianceTool(udb, appliance, name)
		Log("[servitor] capability %q on %s deleted by the owner", name, appliance)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// capabilitiesSectionActions exposes the row actions for assertion. The
// confirm wording is part of the safety story, so it is worth pinning rather
// than trusting to a later edit.
func capabilitiesSectionActions() []ui.RowAction {
	tbl, _ := capabilitiesSection().Body.(ui.Table)
	return tbl.RowActions
}

// capabilitiesSection is the review surface, added to the manage page.
func capabilitiesSection() ui.Section {
	return ui.Section{
		Title: "Requested capabilities",
		Subtitle: "Commands an agent asked to be able to run, and the ones you have already allowed. " +
			"Read the command before approving: an approved capability runs whenever the agent decides to use it, " +
			"without asking again, subject to the risk categories you have granted that agent.",
		Body: ui.Table{
			Source:    "api/appliance-tools",
			RowKey:    "id",
			EmptyText: "Nothing has been requested. An agent asks for a capability when it needs to do something on a system and has no tool for it.",
			Columns: []ui.Col{
				{Field: "name", Label: "Capability", Flex: 2},
				{Field: "appliance", Label: "System", Flex: 2, Mute: true},
				// The widest column, and never muted. This is the thing being
				// approved; everything else on the row is metadata about it.
				{Field: "command", Label: "Runs", Flex: 5},
				{Field: "risk", Label: "Risk", Flex: 1, Mute: true},
				{Field: "requested_by", Label: "Asked by", Flex: 1, Mute: true},
				{Field: "status", Label: "", Flex: 1, Type: "badge", Badges: []ui.BadgeMapping{
					{Value: "pending", Label: "pending", Color: "warning"},
					{Value: "approved", Label: "approved", Color: "success"},
				}},
			},
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Approve", HideIf: "approved",
					PostTo: "api/appliance-tool?appliance={appliance_id}&name={name}&approved=1",
					Method: "POST",
					Confirm: "Approve this capability?\n\nThe agent will be able to run this command on this system whenever it decides to, " +
						"without asking again. Make sure you have read exactly what it runs."},
				{Type: "button", Label: "Revoke", OnlyIf: "approved",
					PostTo: "api/appliance-tool?appliance={appliance_id}&name={name}&approved=0",
					Method: "POST",
					Confirm: "Revoke this capability? The agent keeps the request but can no longer run it."},
				{Type: "button", Label: "Delete", Variant: "danger", Compact: true,
					PostTo:  "api/appliance-tool?appliance={appliance_id}&name={name}",
					Method:  "DELETE",
					Confirm: "Delete this capability entirely? The agent would have to ask for it again."},
			},
		},
	}
}
