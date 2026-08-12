// The appliance form's field definitions.
//
// There used to be a whole "Manage" page here, with its own appliance table and
// its own copy of this form. It was redundant the day it shipped: the chat
// page's toolbar already creates and edits appliances, so Manage duplicated
// that and added a second place to look for the same thing. It is gone, and
// these fields are what remain — the shape the chat page's modal follows.

package servitor

import (
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
				{Value: "bundle", Label: "Evidence bundle (upload)"},
				{Value: "toolset", Label: "Tool-backed service"},
				{Value: "remote", Label: "Remote system (on a peer)"},
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
		// Bundle-only. Nothing to configure at create time: the appliance is
		// minted empty and filled by an upload, since the record has to exist
		// before there is anywhere to send the bytes. The upload affordance is
		// a row action on the appliance list, not a field here.
		//
		// The instructions field below carries the same weight it does for
		// every other type, so a bundle's provenance ("dump from cust-42,
		// pulled 14 Mar after the outage") lives there rather than needing a
		// field of its own.
		// Per-appliance model tier. Two fields rather than one because they are
		// different bets: the orchestrator is one call per round and reasons
		// about the whole investigation, while the workers are the high-volume
		// half that runs the commands — pinning THOSE to the lead is a large
		// cost change and should have to be said on purpose.
		{Type: "header", Label: "Model", Collapsed: true},
		{Field: "orchestrator_tier", Label: "Orchestrator model", Type: "select",
			Options: applianceTierOptions("the orchestrator"),
			Help:    applianceTierHelp("the investigation's reasoning")},
		{Field: "worker_tier", Label: "Worker model", Type: "select",
			Options: applianceTierOptions("the workers"),
			Help:    applianceTierHelp("the workers that run commands on this system")},
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

// applianceTierOptions builds the picker for one of the two per-appliance tier
// fields.
//
// The lead option is OMITTED when the deployment does not permit a lead at all.
// With "All LLMs are private" off, servitor is pinned to the worker no matter
// what this says, so offering "Lead" would be a control that saves, reads back
// correctly and changes nothing — the exact failure this codebase has now hit
// in a routing row, a notes panel and a file picker. The field's help says why
// it is missing, because an option that silently is not there is its own kind
// of confusing.
func applianceTierOptions(what string) []ui.SelectOption {
	out := []ui.SelectOption{
		{Value: "", Label: "Follow routing",
			Help: "Use the global LLM Routing setting for " + what + "."},
		{Value: "worker", Label: "Worker (always)",
			Help: "Pin " + what + " to the local worker even when routing would escalate — for a system where the extra cost is never worth it."},
	}
	if AllLLMsPrivate() {
		out = append(out, ui.SelectOption{Value: "lead", Label: "Lead (always)",
			Help: "Pin " + what + " to the lead model, for a system that keeps defeating the worker."})
	}
	return out
}

// applianceTierHelp explains the field, including why Lead may be absent.
func applianceTierHelp(what string) string {
	base := "Which model handles " + what + " for this appliance, overriding LLM Routing."
	if AllLLMsPrivate() {
		return base + " Lead is selectable because every configured model is private; turning Model Privacy off returns this appliance to the worker."
	}
	return base + " Lead is not offered: Servitor handles credentials and log contents, so it stays on the worker unless every configured model is private (Admin → LLMs → Model Privacy)."
}
