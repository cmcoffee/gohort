// The appliance form's field definitions.
//
// There used to be a whole "Manage" page here, with its own appliance table and
// its own copy of this form. It was redundant the day it shipped: the chat
// page's toolbar already creates and edits appliances, so Manage duplicated
// that and added a second place to look for the same thing. It is gone, and
// these fields are what remain — the shape the chat page's modal follows.

package servitor

import (
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
