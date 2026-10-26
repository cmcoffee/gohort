package admin

import (
	"errors"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerMCPRoutes wires the mcp API under the admin sub-mux.
func (a *AdminApp) registerMCPRoutes(sub *http.ServeMux) {
	// Remote MCP servers — the SERVER-SIDE Model Context Protocol client.
	// GET lists configs (tokens excluded); GET ?name=X returns one for the
	// edit form; POST upserts (or ?action=enable|disable&name=X toggles);
	// DELETE removes. Any mutation calls MCP().Reload() so the change
	// (connect/disconnect, tool registration) takes effect live.
	sub.HandleFunc("/api/mcp-servers", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleMCPServers, a.gate()),
		"POST":   a.mcpServerPost(),
		"DELETE": jsonapi.Delete(a.db, a.handleMCPServerDelete, a.gate()),
	}.Serve)

	// Inbound MCP tool governance: which app-contributed MCP tools are exposed on
	// gohort's own /mcp/ endpoint to external clients. GET lists every registered
	// tool with its exposed state; POST?action=expose|hide&name= flips one. Tools
	// default OFF so adding an app tool never silently widens the surface.
	sub.HandleFunc("/api/mcp-tools", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleMCPTools, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleMCPToolExpose, a.gate()),
	}.Serve)

	// Per-user OAuth connect (hosted MCP servers): start 302-redirects the
	// browser to the authorization server; callback redeems the code. See
	// mcp_oauth.go. Redirects, so they stay plain handlers.
	sub.HandleFunc("/api/mcp-servers/oauth/start", a.handleMCPOAuthStart)

	sub.HandleFunc("/api/mcp-servers/oauth/callback", a.handleMCPOAuthCallback)

	// Inline "Test connection" for an MCP server. The declarative form
	// POSTs its current working state (config + token); we connect,
	// initialize, and tools/list, returning {ok,message}/{ok,error} so the
	// operator can verify reachability + auth before enabling. Never echoes
	// the token.
	sub.HandleFunc("/api/mcp-servers/test", jsonapi.Post(a.db, a.handleMCPServerTest, a.gate()))
}

// handleMCPServers is the remote MCP servers, or the ?name= one for the edit
// form. A token is not part of the config, so none goes out.
func (a *AdminApp) handleMCPServers(c *jsonapi.Call) (any, error) {
	if name := strings.TrimSpace(c.R.URL.Query().Get("name")); name != "" {
		if cfg, ok := MCP().Load(name); ok {
			return cfg, nil
		}
		return nil, jsonapi.NotFound("not found")
	}
	// List rows carry derived flags: is_oauth (so the Connect
	// button shows only for oauth servers) and connected (this
	// admin's own per-user connection status).
	type mcpRow struct {
		MCPServerConfig
		IsOAuth   bool `json:"is_oauth"`
		Connected bool `json:"connected"`
	}
	var rows []mcpRow
	for _, cfg := range MCP().List() {
		rows = append(rows, mcpRow{cfg, cfg.AuthMode == MCPAuthOAuth, MCP().Connected(c.User, cfg.Name)})
	}
	return rows, nil
}

// mcpServerPost tells /api/mcp-servers' two POST shapes apart: a toggle names
// ?action=, the upsert sends the whole config.
func (a *AdminApp) mcpServerPost() http.HandlerFunc {
	toggle := jsonapi.Post(a.db, a.handleMCPServerToggle, a.gate())
	save := jsonapi.Post(a.db, a.handleMCPServerSave, a.gate())
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "" {
			toggle(w, r)
			return
		}
		save(w, r)
	}
}

// handleMCPServerToggle switches the ?name= server on or off.
func (a *AdminApp) handleMCPServerToggle(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	cfg, ok := MCP().Load(name)
	if !ok {
		return nil, jsonapi.NotFound("not found")
	}
	switch q.Get("action") {
	case "enable":
		cfg.Enabled = true
	case "disable":
		cfg.Enabled = false
	default:
		return nil, jsonapi.BadRequest("action must be enable|disable")
	}
	if err := MCP().Save(cfg, ""); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	MCP().Reload()
	return jsonapi.Empty(http.StatusNoContent), nil
}

// mcpServerSave is the MCP server form: the config, and the secrets kept
// apart from it.
type mcpServerSave struct {
	MCPServerConfig
	Token             string `json:"token"`
	OAuthClientSecret string `json:"oauth_client_secret"`
}

// handleMCPServerSave creates or saves a server and reloads the client.
func (a *AdminApp) handleMCPServerSave(c *jsonapi.Call, body mcpServerSave) (any, error) {
	if err := MCP().Save(body.MCPServerConfig, body.Token); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// Manual OAuth client secret (non-DCR fallback) — stored encrypted,
	// separately from the config; blank keeps any existing one.
	MCP().SetOAuthClientSecret(body.Name, body.OAuthClientSecret)
	MCP().Reload()
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMCPServerDelete removes the ?name= server.
func (a *AdminApp) handleMCPServerDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if err := MCP().Delete(name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMCPTools is every app MCP tool and whether it is exposed.
func (a *AdminApp) handleMCPTools(c *jsonapi.Call) (any, error) {
	return MCPAppToolStatuses(), nil
}

// handleMCPToolExpose exposes or hides the ?name= app tool; the On/Off toggle
// posts {exposed: bool}.
func (a *AdminApp) handleMCPToolExpose(c *jsonapi.Call, body struct {
	Exposed bool `json:"exposed"`
}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if _, ok := LookupMCPTool(name); !ok {
		return nil, jsonapi.NotFound("no such MCP tool")
	}
	SetMCPAppToolExposed(name, body.Exposed)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMCPServerTest connects to a server with the form's working config.
func (a *AdminApp) handleMCPServerTest(c *jsonapi.Call, body struct {
	MCPServerConfig
	Token string `json:"token"`
}) (any, error) {
	// Under the request's context: the form's Cancel closes the request
	// and the handshake is dropped with it.
	ctx := c.R.Context()
	msg, err := MCP().Test(ctx, body.MCPServerConfig, body.Token)
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("cancelled before the server finished the handshake")
		}
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	return map[string]any{"ok": true, "message": msg}, nil
}
