package extensions

// A person's own MCP servers (core/mcppersonal): added here, used by their
// own agents. Whether they may add one at all is the administrator's policy
// (core/governance).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/mcppersonal"
	"github.com/cmcoffee/gohort/core/ui"
)

// mcpServerRow is one server as the list shows it. The token never leaves.
type mcpServerRow struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Auth     string `json:"auth"`
	Tools    string `json:"tools"`
	Problem  string `json:"problem,omitempty"`
	Disabled bool   `json:"disabled"`
	Held     bool   `json:"held"`
	Awaiting bool   `json:"awaiting"`
	// CanEnable / CanDisable are the owner's switch, where it is theirs.
	CanEnable  bool `json:"can_enable"`
	CanDisable bool `json:"can_disable"`
	OneTool    bool `json:"one_tool"`
	ManyTools  bool `json:"many_tools"`
}

func mcpRow(s mcppersonal.Server) mcpServerRow {
	tools := "not checked yet"
	if !s.CheckedAt.IsZero() {
		tools = fmt.Sprintf("%d", len(s.Tools))
	}
	held := s.AdminDisabled && !s.AwaitingApproval
	return mcpServerRow{
		Name: s.Name, URL: s.URL, Auth: s.SignIn(), Tools: tools, Problem: s.CheckErr,
		Disabled: !s.Usable(), Held: held, Awaiting: s.AwaitingApproval,
		CanEnable:  s.Disabled && !s.AdminDisabled && !s.AwaitingApproval,
		CanDisable: !s.Disabled && !s.AdminDisabled && !s.AwaitingApproval,
		OneTool:    s.OneTool,
		ManyTools:  !s.OneTool,
	}
}

// handleMCPServers is the person's own MCP servers:
//
//	GET                       the list
//	GET ?options=credentials  the API credentials one may sign in with
//	POST {name,url,auth,token} add or update one, then check it
//	POST ?action=check|enable|disable&name=
//	DELETE ?name=
func (T *Extensions) handleMCPServers(c *jsonapi.Call) (any, error) {
	user := c.User
	if c.R.URL.Query().Get("options") == "credentials" {
		return credentialOptions(user), nil
	}
	rows := []mcpServerRow{}
	for _, s := range mcppersonal.List(user) {
		rows = append(rows, mcpRow(s))
	}
	return rows, nil
}

// mcpServerPost tells /api/mcp-servers' POST shapes apart: ?action= acts on
// one server, no action adds or updates one.
func (T *Extensions) mcpServerPost() http.HandlerFunc {
	act := jsonapi.Post(T.DB, T.handleMCPServerAction)
	save := jsonapi.Post(T.DB, T.handleMCPServerSave, jsonapi.MaxBody(1<<16))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Query().Get("action")) != "" {
			act(w, r)
			return
		}
		save(w, r)
	}
}

// handleMCPServerAction checks, switches, or sets how the ?name= server's
// tools are offered.
func (T *Extensions) handleMCPServerAction(c *jsonapi.Call, _ struct{}) (any, error) {
	user, q := c.User, c.R.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	switch action := strings.TrimSpace(q.Get("action")); action {
	case "check":
		ctx, cancel := context.WithTimeout(c.R.Context(), 45*time.Second)
		defer cancel()
		s, err := mcppersonal.Check(ctx, user, name)
		if err != nil {
			return nil, jsonapi.Status(http.StatusBadGateway, "could not reach it: "+err.Error())
		}
		return map[string]string{"message": fmt.Sprintf("%s answered with %d tool(s).", s.Name, len(s.Tools))}, nil
	case "enable", "disable":
		if err := mcppersonal.SetDisabled(user, name, action == "disable"); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	case "one_tool", "many_tools":
		if err := mcppersonal.SetOneTool(user, name, action == "one_tool"); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	default:
		return nil, jsonapi.BadRequest("action must be check, enable, disable, one_tool or many_tools")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// mcpServerSave is the MCP server form.
type mcpServerSave struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Auth       string `json:"auth"`
	Token      string `json:"token"`
	Credential string `json:"credential"`
	ClientID   string `json:"oauth_client_id"`
	Secret     string `json:"oauth_client_secret"`
	Scopes     string `json:"oauth_scopes"`
	OneTool    bool   `json:"one_tool"`
}

// handleMCPServerSave adds or updates a server, then checks it.
func (T *Extensions) handleMCPServerSave(c *jsonapi.Call, body mcpServerSave) (any, error) {
	user := c.User
	body.Name = strings.TrimSpace(body.Name)
	// A NEW one answers to the administrator's policy; an edit to one
	// they have is not adding one.
	// Pointing one at another server is adding that server, as far as an
	// approval goes: it waits for an administrator where new ones do, and
	// where only an administrator adds them.
	var hold bool
	if prev, exists := mcppersonal.Load(user, body.Name); !exists {
		var perr error
		if hold, perr = governance.Permit(governance.MCPServers, user); perr != nil {
			return nil, jsonapi.Forbidden(perr.Error())
		}
	} else if !mcppersonal.SameOrigin(prev.URL, body.URL) {
		h, perr := governance.Permit(governance.MCPServers, user)
		hold = h || perr != nil
	}
	// The secret goes with the sign-in it is for: a token typed and then
	// left behind on a switch to OAuth is not the client's secret.
	secret := body.Token
	if body.Auth == mcppersonal.AuthOAuth {
		secret = body.Secret
	}
	srv := mcppersonal.Server{Owner: user, Name: body.Name, URL: body.URL, Auth: body.Auth, Credential: body.Credential,
		OAuthClientID: body.ClientID, OAuthScopes: body.Scopes, OneTool: body.OneTool}
	if err := mcppersonal.Save(srv, secret, hold); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// What it offers, now, so the list (and an approving administrator)
	// can see it. A server that does not answer is saved all the same.
	ctx, cancel := context.WithTimeout(c.R.Context(), 45*time.Second)
	defer cancel()
	if body.Auth == mcppersonal.AuthOAuth && !mcppersonal.Connected(user, body.Name) {
		msg := fmt.Sprintf("Added %s. Sign in to it next: press Connect beside it under Connected accounts, and its tools reach your agents once you have.", body.Name)
		if s, _ := mcppersonal.Load(user, body.Name); s.AwaitingApproval {
			msg += " Personal MCP servers need an administrator's approval here, too."
		}
		return map[string]string{"message": msg}, nil
	}
	s, err := mcppersonal.Check(ctx, user, body.Name)
	msg := fmt.Sprintf("Added %s: it answered with %d tool(s).", s.Name, len(s.Tools))
	if err != nil {
		msg = fmt.Sprintf("Added %s, but it did not answer (%v): check its address and how it signs in, then press Check.", s.Name, err)
	}
	if s.AwaitingApproval {
		msg += " Personal MCP servers need an administrator's approval here: its tools reach your agents once one approves it."
	}
	return map[string]string{"message": msg}, nil
}

// handleMCPServerDelete removes the ?name= server.
func (T *Extensions) handleMCPServerDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := mcppersonal.Remove(c.User, strings.TrimSpace(c.R.URL.Query().Get("name"))); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// mcpRowActions are a server's actions on its row under Integrations.
func mcpRowActions() []ui.RowAction {
	return []ui.RowAction{
		{Type: "button", Label: "Check", Method: "POST", PostTo: "api/mcp-servers?action=check&name={name}"},
		{Type: "button", Label: "Enable", Method: "POST", PostTo: "api/mcp-servers?action=enable&name={name}", OnlyIf: "can_enable"},
		{Type: "button", Label: "Disable", Method: "POST", PostTo: "api/mcp-servers?action=disable&name={name}", OnlyIf: "can_disable"},
		{Type: "button", Label: "Offer as one tool", Method: "POST", PostTo: "api/mcp-servers?action=one_tool&name={name}", OnlyIf: "many_tools"},
		{Type: "button", Label: "Offer tools separately", Method: "POST", PostTo: "api/mcp-servers?action=many_tools&name={name}", OnlyIf: "one_tool"},
		{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "api/mcp-servers?name={name}", Variant: "danger",
			Confirm: "Delete this MCP server? Your agents lose its tools, and its token is removed."},
	}
}

// mcpAddFields are the form a server is added with.
func mcpAddFields() []ui.FormField {
	callback := "<this site's address>/account/mcp/callback"
	if db := AuthDB(); db != nil {
		var ext string
		if db.Get(WebTable, "external_url", &ext) && strings.TrimSpace(ext) != "" {
			callback = strings.TrimRight(strings.TrimSpace(ext), "/") + "/account/mcp/callback"
		}
	}
	return []ui.FormField{
		{Field: "name", Label: "Name", Required: true, Placeholder: "e.g. linear",
			Help: "Lowercase letters, digits, dashes or underscores. Its tools are named <name>_<tool>."},
		{Field: "url", Label: "Address", Required: true, Placeholder: "https://mcp.example.com/mcp",
			Help: "The server's Streamable HTTP endpoint. Public https addresses only."},
		{Field: "auth", Label: "Signs in with", Type: "select", Options: []ui.SelectOption{
			{Value: "", Label: "Nothing: an open server"},
			{Value: "bearer", Label: "A bearer token"},
			{Value: "secure_api", Label: "One of your API credentials"},
			{Value: "oauth", Label: "OAuth: you sign in"},
		}, Help: "With OAuth you sign in to the provider after saving, under Connected accounts."},
		{Field: "token", Label: "Token", Type: "password", ShowWhen: "auth:bearer",
			Help: "Stored encrypted, sent only to this server, never shown to an agent."},
		{Field: "credential", Label: "API credential", Type: "select", ShowWhen: "auth:secure_api",
			OptionsSource: "api/mcp-servers?options=credentials",
			Help:          "Its key signs every request, held to the credential's own rules: its allowed endpoints must cover this server's address, and it may not ask before writes."},
		{Field: "oauth_client_id", Label: "Client ID (only if the provider needs one)", ShowWhen: "auth:oauth",
			Help: "Leave it blank: most providers register a client for you when you sign in.",
			Detail: "Some providers do not register clients themselves, and signing in then says so. For one of those, register an OAuth app with the provider, " +
				"give it the redirect URI " + callback + ", and paste its client ID here."},
		{Field: "oauth_client_secret", Label: "Client secret (optional)", Type: "password", ShowWhen: "auth:oauth;oauth_client_id",
			Help: "Only if the provider gave your app one. Stored encrypted, sent only to the provider's token endpoint."},
		{Field: "oauth_scopes", Label: "Scopes (optional)", ShowWhen: "auth:oauth",
			Help: "Space-separated. Leave blank to ask for what the server advertises."},
		{Field: "one_tool", Label: "Offer as one tool", Type: "toggle",
			Help: "Your agents see one tool named for the server, with an action per tool, instead of each tool on its own. For a server with many tools."},
	}
}

// credentialOptions are the API credentials user may sign a server's requests
// with: their own, then the deployment's they may use. One with no key to
// send, or switched off, is left out.
func credentialOptions(user string) []ui.SelectOption {
	opts := []ui.SelectOption{}
	seen := map[string]bool{}
	for _, c := range Secure().ListUser(user) {
		if c.Disabled || c.Type == SecureCredNone || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		opts = append(opts, ui.SelectOption{Value: c.Name, Label: c.Name + " (yours)"})
	}
	for _, c := range Secure().List() {
		if c.Disabled || c.Type == SecureCredNone || seen[c.Name] || !Secure().UserMayUse(c, user) {
			continue
		}
		seen[c.Name] = true
		opts = append(opts, ui.SelectOption{Value: c.Name, Label: c.Name})
	}
	return opts
}
