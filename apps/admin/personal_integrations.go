package admin

// Personal integrations: the administrator's say over whether people add
// their own API credentials and MCP servers (core/governance).

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/ui"
)

// handlePersonalIntegrations is GET / POST /api/personal-integrations: the
// policy for each kind, {"credentials": "allowed", "mcp_servers": "approval"}.
// A POST may carry either or both.
func (a *AdminApp) handlePersonalIntegrations(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for _, k := range governance.Kinds {
			v, ok := body[string(k)].(string)
			if !ok || governance.Policy(v) == governance.Of(k) {
				continue
			}
			if err := governance.Set(k, v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			adminChange(r, activity.Settings, "set personal %s policy=%s", k, v)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := map[string]string{}
	for _, k := range governance.Kinds {
		out[string(k)] = string(governance.Of(k))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// policyOptions are a policy select's choices, with what each means for kind.
func policyOptions(noun string) []ui.SelectOption {
	return []ui.SelectOption{
		{Value: string(governance.Allowed), Label: "Allowed: people add their own " + noun + " and use them at once"},
		{Value: string(governance.NeedsApproval), Label: "Needs approval: they add their own, held off until an administrator approves each"},
		{Value: string(governance.AdminOnly), Label: "Admin only: the administrator sets these up; nobody else adds one"},
	}
}

// personalIntegrationsSection is the Governance section that sets them.
func personalIntegrationsSection() ui.Section {
	return ui.Section{
		Title:    "Personal integrations",
		Subtitle: "Whether people add their own API credentials and MCP servers, or the administrator sets them up.",
		Detail: "A policy governs adding a new one. What someone already has keeps working; turn any of it off below, in User-owned credentials and Personal MCP servers.\n\n" +
			"Needs approval holds each new one off until you enable it below. An administrator is never held by either policy.\n\n" +
			"A personal MCP server's tools reach only its owner's agents, and it may not point at this network's private addresses unless its owner is an administrator.",
		Body: ui.FormPanel{
			Source: "api/personal-integrations",
			Fields: []ui.FormField{
				{Field: string(governance.Credentials), Label: "Personal API credentials", Type: "select",
					Options: policyOptions("API credentials"),
					Help:    "People add these on their Extensions page, or Builder drafts one for them."},
				{Field: string(governance.MCPServers), Label: "Personal MCP servers", Type: "select",
					Options: policyOptions("MCP servers"),
					Help:    "People add these on their Extensions page. Their tools reach only the owner's agents."},
			},
		},
	}
}
