package admin

// Personal integrations: the administrator's say over whether people add
// their own API credentials and MCP servers (core/governance).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/mcppersonal"
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

// personalMCPRow is one person's MCP server as the administrator sees it.
type personalMCPRow struct {
	ID       string `json:"id"` // owner/name, unique across people
	Owner    string `json:"owner"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	SignIn   string `json:"sign_in"`
	Tools    string `json:"tools"`
	Awaiting bool   `json:"awaiting"`
	Disabled bool   `json:"disabled"`
	Problem  string `json:"problem,omitempty"`
}

// handlePersonalMCPServers is every person's own MCP servers:
//
//	GET                                     the list
//	POST ?action=enable|disable&owner=&name=  enable approves one that waits
//	POST ?action=check&owner=&name=           ask it what it offers
//	DELETE ?owner=&name=
func (a *AdminApp) handlePersonalMCPServers(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	switch r.Method {
	case http.MethodGet:
		rows := []personalMCPRow{}
		for _, s := range mcppersonal.All() {
			tools := "not checked yet"
			if !s.CheckedAt.IsZero() {
				names := make([]string, 0, len(s.Tools))
				for _, t := range s.Tools {
					names = append(names, t.Name)
				}
				tools = fmt.Sprintf("%d: %s", len(s.Tools), strings.Join(names, ", "))
			}
			rows = append(rows, personalMCPRow{ID: s.Owner + "/" + s.Name, Owner: s.Owner, Name: s.Name, URL: s.URL, SignIn: s.SignIn(),
				Tools: tools, Awaiting: s.AwaitingApproval, Disabled: !s.Usable(), Problem: s.CheckErr})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	case http.MethodPost:
		var err error
		switch action := r.URL.Query().Get("action"); action {
		case "enable", "disable":
			if err = mcppersonal.AdminSetDisabled(owner, name, action == "disable"); err == nil {
				adminChange(r, activity.Settings, "%s personal MCP server %s/%s", action, owner, name)
			}
		case "check":
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			_, err = mcppersonal.Check(ctx, owner, name)
		default:
			http.Error(w, "action must be enable, disable or check", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := mcppersonal.Remove(owner, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		adminChange(r, activity.Settings, "deleted personal MCP server %s/%s", owner, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// personalMCPServersSection is the Governance list of them.
func personalMCPServersSection() ui.Section {
	return ui.Section{
		Title:    "Personal MCP servers",
		Subtitle: "MCP servers people add for themselves, on their Extensions page.",
		Detail: "Each one's tools reach only its owner's agents, in the owner's own runs. Approve enables one that is waiting; Disable holds one off, and the owner cannot switch it back on. " +
			"The tools column is what the server offered when it last answered, so you can see what you are approving; Check asks it again.",
		Body: ui.Table{
			Source: "api/personal-mcp-servers",
			RowKey: "id",
			Columns: []ui.Col{
				{Field: "owner", Label: "Owner"},
				{Field: "name", Flex: 1},
				{Field: "url", Label: "Address", Mute: true, Flex: 2},
				{Field: "sign_in", Label: "Signs in with", Mute: true},
				{Field: "tools", Label: "Tools", Mute: true, Flex: 2},
				{Field: "awaiting", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: true, Label: "Awaiting approval", Color: "warning"},
				}},
				{Field: "disabled", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: true, Label: "Off", Color: "danger"},
					{Value: false, Label: "Enabled", Color: "success"},
				}},
			},
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Approve", Method: "POST", PostTo: "api/personal-mcp-servers?action=enable&owner={owner}&name={name}", OnlyIf: "awaiting"},
				{Type: "button", Label: "Enable", Method: "POST", PostTo: "api/personal-mcp-servers?action=enable&owner={owner}&name={name}", OnlyIf: "disabled", HideIf: "awaiting"},
				{Type: "button", Label: "Disable", Method: "POST", PostTo: "api/personal-mcp-servers?action=disable&owner={owner}&name={name}", HideIf: "disabled"},
				{Type: "button", Label: "Check", Method: "POST", PostTo: "api/personal-mcp-servers?action=check&owner={owner}&name={name}"},
				{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "api/personal-mcp-servers?owner={owner}&name={name}", Variant: "danger",
					Confirm: "Delete this person's MCP server? Their agents lose its tools and its token is removed."},
			},
			EmptyText: "No personal MCP servers. When someone adds one on their Extensions page, it appears here.",
		},
	}
}
