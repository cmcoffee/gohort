package admin

// Personal integrations: the administrator's say over whether people add
// their own API credentials, MCP servers and Teams bots (core/governance).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/mcppersonal"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/webhooks"
)

// handlePersonalIntegrations is GET / POST /api/personal-integrations: the
// policy for each kind, {"credentials": "allowed", "mcp_servers": "approval"}.
// A POST may carry either or both.
func (a *AdminApp) handlePersonalIntegrations(c *jsonapi.Call) (any, error) {
	out := map[string]string{}
	for _, k := range governance.Kinds {
		out[string(k)] = string(governance.Of(k))
	}
	return out, nil
}

// handlePersonalIntegrationsSave sets the policies the body names, and
// answers with every one.
func (a *AdminApp) handlePersonalIntegrationsSave(c *jsonapi.Call, body map[string]any) (any, error) {
	for _, k := range governance.Kinds {
		v, ok := body[string(k)].(string)
		if !ok || governance.Policy(v) == governance.Of(k) {
			continue
		}
		if err := governance.Set(k, v); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		adminChange(c.R, activity.Settings, "set personal %s policy=%s", k, v)
	}
	return a.handlePersonalIntegrations(c)
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
		Subtitle: "Whether people add their own API credentials, MCP servers and Teams bots, or the administrator sets them up.",
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
				{Field: string(governance.TeamsBots), Label: "Personal Teams bots", Type: "select",
					Options: policyOptions("Teams bots"),
					Help:    "People add these under Extensions > Integrations, from a bot they registered in Azure. Each answers in Teams with its owner's agents."},
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
func (a *AdminApp) handlePersonalMCPServers(c *jsonapi.Call) (any, error) {
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
	return rows, nil
}

// handlePersonalMCPServerAction switches one user's MCP server on or off, or
// checks it now.
func (a *AdminApp) handlePersonalMCPServerAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner, name := strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("name"))
	var err error
	switch action := q.Get("action"); action {
	case "enable", "disable":
		if err = mcppersonal.AdminSetDisabled(owner, name, action == "disable"); err == nil {
			adminChange(c.R, activity.Settings, "%s personal MCP server %s/%s", action, owner, name)
		}
	case "check":
		ctx, cancel := context.WithTimeout(c.R.Context(), 45*time.Second)
		defer cancel()
		_, err = mcppersonal.Check(ctx, owner, name)
	default:
		return nil, jsonapi.BadRequest("action must be enable, disable or check")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handlePersonalMCPServerDelete removes one user's MCP server.
func (a *AdminApp) handlePersonalMCPServerDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner, name := strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("name"))
	if err := mcppersonal.Remove(owner, name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "deleted personal MCP server %s/%s", owner, name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// personalTeamsBotRow is one person's Teams bot as the administrator sees it.
type personalTeamsBotRow struct {
	ID       string `json:"id"` // owner/service
	Owner    string `json:"owner"`
	Service  string `json:"service"`
	Name     string `json:"name"`
	AppID    string `json:"app_id"`
	Tenant   string `json:"tenant"`
	Answers  string `json:"answers"`
	Awaiting bool   `json:"awaiting"`
	Disabled bool   `json:"disabled"`
}

// handlePersonalTeamsBots is every person's own Teams bots:
//
//	GET                                          the list
//	POST ?action=enable|disable&owner=&service=  enable approves one that waits
//	DELETE ?owner=&service=
func (a *AdminApp) handlePersonalTeamsBots(c *jsonapi.Call) (any, error) {
	rows := []personalTeamsBotRow{}
	for _, b := range teamsbots.All() {
		tenant, answers := b.TenantID, strings.Join(b.Accept, ", ")
		if tenant == "" {
			tenant = "any (multi-tenant)"
		}
		if answers == "" {
			answers = strings.Join(teamsbots.ConversationTypes, ", ")
		}
		rows = append(rows, personalTeamsBotRow{ID: b.Owner + "/" + b.Service, Owner: b.Owner, Service: b.Service, Name: b.Name,
			AppID: b.AppID, Tenant: tenant, Answers: answers, Awaiting: b.AwaitingApproval, Disabled: !b.Usable()})
	}
	return rows, nil
}

// handlePersonalTeamsBotAction approves, enables or switches off one.
func (a *AdminApp) handlePersonalTeamsBotAction(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner, svc := strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("service"))
	action := q.Get("action")
	if action != "enable" && action != "disable" {
		return nil, jsonapi.BadRequest("action must be enable or disable")
	}
	if err := teamsbots.AdminSetDisabled(owner, svc, action == "disable"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "%s personal Teams bot %s/%s", action, owner, svc)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handlePersonalTeamsBotDelete removes one, with the webhook it is reached
// through.
func (a *AdminApp) handlePersonalTeamsBotDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	owner, svc := strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("service"))
	b, ok := teamsbots.Load(owner, svc)
	if !ok {
		return nil, jsonapi.BadRequest(owner + " has no bot for " + svc)
	}
	teamsbots.Remove(owner, svc)
	if b.Webhook != "" {
		webhooks.Remove(owner, b.Webhook)
	}
	adminChange(c.R, activity.Settings, "deleted personal Teams bot %s/%s", owner, svc)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// personalTeamsBotsSection is the Governance list of them.
func personalTeamsBotsSection() ui.Section {
	return ui.Section{
		Title:    "Personal Teams bots",
		Subtitle: "Microsoft Teams bots people add for themselves, under Extensions > Integrations.",
		Detail: "Each is a bot its owner registered in Azure. It answers in Teams with its owner's agents, in the conversations they let it, and replies under the bot's own name. " +
			"Approve enables one that is waiting; Disable holds one off, and the owner cannot switch it back on. Check the app id is one your organisation knows before approving it.",
		Body: ui.Table{
			Source: "api/personal-teams-bots",
			RowKey: "id",
			Columns: []ui.Col{
				{Field: "owner", Label: "Owner"},
				{Field: "name", Flex: 1},
				{Field: "service", Label: "Service", Mute: true},
				{Field: "app_id", Label: "App id", Mute: true, Flex: 2},
				{Field: "tenant", Label: "Tenant", Mute: true, Flex: 1},
				{Field: "answers", Label: "Answers in", Mute: true},
				{Field: "awaiting", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: true, Label: "Awaiting approval", Color: "warning"},
				}},
				{Field: "disabled", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: true, Label: "Off", Color: "danger"},
					{Value: false, Label: "Enabled", Color: "success"},
				}},
			},
			RowActions: []ui.RowAction{
				{Type: "button", Label: "Approve", Method: "POST", PostTo: "api/personal-teams-bots?action=enable&owner={owner}&service={service}", OnlyIf: "awaiting"},
				{Type: "button", Label: "Enable", Method: "POST", PostTo: "api/personal-teams-bots?action=enable&owner={owner}&service={service}", OnlyIf: "disabled", HideIf: "awaiting"},
				{Type: "button", Label: "Disable", Method: "POST", PostTo: "api/personal-teams-bots?action=disable&owner={owner}&service={service}", HideIf: "disabled"},
				{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "api/personal-teams-bots?owner={owner}&service={service}", Variant: "danger",
					Confirm: "Delete this person's Teams bot? It stops answering, and its secret and webhook are removed."},
			},
			EmptyText: "No personal Teams bots. When someone adds one under Extensions > Integrations, it appears here.",
		},
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
