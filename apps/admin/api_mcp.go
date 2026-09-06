package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerMCPRoutes wires the mcp API under the admin sub-mux.
func (a *AdminApp) registerMCPRoutes(sub *http.ServeMux) {
	// Remote MCP servers — the SERVER-SIDE Model Context Protocol client.
	// GET lists configs (tokens excluded); GET ?name=X returns one for the
	// edit form; POST upserts (or ?action=enable|disable&name=X toggles);
	// DELETE removes. Any mutation calls MCP().Reload() so the change
	// (connect/disconnect, tool registration) takes effect live.
	sub.HandleFunc("/api/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
				if c, ok := MCP().Load(name); ok {
					json.NewEncoder(w).Encode(c) // token is not part of the struct
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// List rows carry derived flags: is_oauth (so the Connect
			// button shows only for oauth servers) and connected (this
			// admin's own per-user connection status).
			user := AuthCurrentUser(r)
			type mcpRow struct {
				MCPServerConfig
				IsOAuth   bool `json:"is_oauth"`
				Connected bool `json:"connected"`
			}
			var rows []mcpRow
			for _, c := range MCP().List() {
				rows = append(rows, mcpRow{c, c.AuthMode == MCPAuthOAuth, MCP().Connected(user, c.Name)})
			}
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			if action := r.URL.Query().Get("action"); action != "" {
				name := strings.TrimSpace(r.URL.Query().Get("name"))
				if name == "" {
					http.Error(w, "missing name", http.StatusBadRequest)
					return
				}
				c, ok := MCP().Load(name)
				if !ok {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				switch action {
				case "enable":
					c.Enabled = true
				case "disable":
					c.Enabled = false
				default:
					http.Error(w, "action must be enable|disable", http.StatusBadRequest)
					return
				}
				if err := MCP().Save(c, ""); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				MCP().Reload()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body struct {
				MCPServerConfig
				Token             string `json:"token"`
				OAuthClientSecret string `json:"oauth_client_secret"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := MCP().Save(body.MCPServerConfig, body.Token); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Manual OAuth client secret (non-DCR fallback) — stored encrypted,
			// separately from the config; blank keeps any existing one.
			MCP().SetOAuthClientSecret(body.Name, body.OAuthClientSecret)
			MCP().Reload()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := MCP().Delete(name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inbound MCP tool governance: which app-contributed MCP tools are exposed on
	// gohort's own /mcp/ endpoint to external clients. GET lists every registered
	// tool with its exposed state; POST?action=expose|hide&name= flips one. Tools
	// default OFF so adding an app tool never silently widens the surface.
	sub.HandleFunc("/api/mcp-tools", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(MCPAppToolStatuses())
		case http.MethodPost:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if _, ok := LookupMCPTool(name); !ok {
				http.Error(w, "no such MCP tool", http.StatusNotFound)
				return
			}
			// The On/Off toggle posts {exposed: bool}.
			var body struct {
				Exposed bool `json:"exposed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			SetMCPAppToolExposed(name, body.Exposed)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inline "Test connection" for an MCP server. The declarative form
	// POSTs its current working state (config + token); we connect,
	// initialize, and tools/list, returning {ok,message}/{ok,error} so the
	// operator can verify reachability + auth before enabling. Never echoes
	// the token.
	// Per-user OAuth connect (hosted MCP servers): start 302-redirects the
	// browser to the authorization server; callback redeems the code. See
	// mcp_oauth.go.
	sub.HandleFunc("/api/mcp-servers/oauth/start", a.handleMCPOAuthStart)

	sub.HandleFunc("/api/mcp-servers/oauth/callback", a.handleMCPOAuthCallback)

	sub.HandleFunc("/api/mcp-servers/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			MCPServerConfig
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		msg, err := MCP().Test(body.MCPServerConfig, body.Token)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
	})

}
