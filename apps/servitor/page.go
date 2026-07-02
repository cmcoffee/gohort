// Framework-based Servitor admin page. Phase 1 of the servitor port —
// covers the credential / appliance management surfaces only. The
// chat / agent-loop UI stays at "/" until a generic agent-loop
// primitive lands; until then, this page lives at "/manage" and the
// legacy chat keeps its current URL.

package servitor

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// applianceFields are reused for both the per-row Edit form and the
// top-of-page "Add appliance" form so the two surfaces stay aligned.
// The list endpoint at /api/appliances accepts POST for both create
// (no id field) and update (id present); the load endpoint at
// /api/appliance/{id} returns the row with the password redacted.
// Combined with FormPanel.PostURL, the edit form GETs the row and
// POSTs back to the list endpoint without server-side wiring.
func applianceFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Label: "Name", Type: "text",
			Placeholder: "Short label shown in the appliance list."},
		{Field: "type", Label: "Type", Type: "select",
			Options: []ui.SelectOption{
				{Value: "ssh", Label: "SSH host"},
				{Value: "command", Label: "Local command"},
				{Value: "repo", Label: "Git repository"},
			}},
		// SSH-only fields. ShowWhen value-matches type==ssh so only SSH
		// rows show host/port/user/password.
		{Field: "host", Label: "Host", Type: "text",
			Placeholder: "hostname or IP", ShowWhen: "type:ssh"},
		{Field: "port", Label: "Port", Type: "number",
			Placeholder: "22", ShowWhen: "type:ssh", Min: 1, Max: 65535},
		{Field: "user", Label: "SSH user", Type: "text",
			Placeholder: "root", ShowWhen: "type:ssh"},
		{Field: "password", Label: "Password (leave blank to keep current)", Type: "password",
			Help:     "Stored encrypted. Editing an existing appliance with this blank keeps the previously-saved password.",
			ShowWhen: "type:ssh"},
		// Command-only fields.
		{Field: "command", Label: "Command (local mode)", Type: "text",
			Placeholder: "kubectl, gh, etc.",
			Help:        "Only used when Type=Local command.", ShowWhen: "type:command"},
		{Field: "work_dir", Label: "Working directory (optional)", Type: "text",
			Placeholder: "/path/to/wd", ShowWhen: "type:command"},
		// Repo-only fields. The repository is cloned into tmpfs and its text
		// files ingested into a hardware-locked encrypted store; the plaintext
		// clone is discarded. Ask it questions like an SSH appliance.
		{Field: "repo_url", Label: "Git URL", Type: "text",
			Placeholder: "https://github.com/owner/repo", ShowWhen: "type:repo"},
		{Field: "repo_branch", Label: "Branch (optional)", Type: "text",
			Placeholder: "default branch if blank", ShowWhen: "type:repo"},
		{Field: "repo_token", Label: "Access token (optional)", Type: "password",
			Help:     "For private repositories. Stored encrypted.",
			ShowWhen: "type:repo"},
		// Shared persona + instruction fields.
		{Field: "persona_name", Label: "Persona name", Type: "text",
			Placeholder: "Support, QA, …",
			Help:        "Short label shown alongside agent replies for this appliance."},
		{Field: "persona_prompt", Label: "Persona prompt", Type: "textarea", Rows: 3,
			Placeholder: "How the agent should approach this appliance."},
		{Field: "instructions", Label: "Instructions", Type: "textarea", Rows: 3,
			Placeholder: "Freeform notes injected into every chat session for this appliance."},
		{Field: "shared", Label: "Shared with all users", Type: "toggle",
			Help: "Everyone can open and use it (with the stored credentials); chat sessions stay per-user. Only you or an admin can change or delete it."},
	}
}

func (T *Servitor) handleManagePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Servitor — Manage",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		Sections: []ui.Section{
			{
				Title:    "Add appliance",
				Subtitle: "SSH host or local command for servitor to probe and run against. Auto-saves field-by-field on blur; a new row appears in the list below as soon as name + host (or command) are valid.",
				Body: ui.FormPanel{
					// Empty Source → starts with a blank record. Saves
					// post to /api/appliances; the handler creates a
					// fresh record on first valid save and updates it
					// on subsequent field changes.
					PostURL: "api/appliances",
					Method:  "POST",
					Fields:  applianceFields(),
				},
			},
			{
				Title:    "Appliances",
				Subtitle: "Existing SSH hosts and local commands. Expand a row to edit; Delete also wipes the appliance's accumulated knowledge (facts, techniques, profile, log map).",
				Body: ui.Table{
					Source:    "api/appliances",
					RowKey:    "id",
					SortBy:    "name",
					EmptyText: "No appliances configured yet. Fill in the form above to add one.",
					Columns: []ui.Col{
						{Field: "name", Label: "Name", Flex: 2},
						{Field: "type", Label: "Type", Flex: 1, Mute: true},
						{Field: "host", Label: "Host", Flex: 2, Mute: true},
						{Field: "scanned", Label: "Last scan", Flex: 1, Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						// Edit expand — GET from per-id endpoint (password
						// redacted server-side) and POST back to the
						// list endpoint, which handles update by ID.
						ui.Expand("Edit", ui.FormPanel{
							Source:  "api/appliance/{id}",
							PostURL: "api/appliances",
							Method:  "POST",
							Fields:  applianceFields(),
						}),
						// Delete uses the path-style endpoint so the
						// framework's empty-body button RowAction works
						// without a wrapper. handleAppliance accepts DELETE.
						{Type: "button", Label: "Delete",
							PostTo: "api/appliance/{id}",
							Method: "DELETE", Variant: "danger",
							Confirm: "Delete this appliance and all its accumulated knowledge (facts, techniques, profile, log map)? Cannot be undone."},
					},
				},
			},
		},
		Footer:    "Servitor chat →",
		FooterURL: ".",
	}
	page.ServeHTTP(w, r)
}
