package mcpserver

// One app as an MCP server of its own, at /mcp/app/<slug>/: an outside client
// connects to the app, not to gohort. The handshake names the server after the
// app and gives its description as instructions, so a client lists it as the
// app; tools/list holds that app's tools alone (no agents, no built-ins, no
// other app), and a call runs as the owner's own click would. The app's agent
// is never reached here, except through an action of the app's that uses it.
//
// The key decides whose app it is: the owner's own app by that slug, and only
// when the key reaches it (a key made for other apps does not). An MCP key
// that names no app reaches this one as it reaches all of its owner's apps.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

// appSlugFromPath is the app named by a request's path, "/app/<slug>/" as the
// endpoint's mux sees it (or with the /mcp mount still on it); "" for none.
func appSlugFromPath(path string) string {
	i := strings.Index(path, "/app/")
	if i < 0 {
		return ""
	}
	rest := path[i+len("/app/"):]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// appServerPath is the app's own MCP address under the site.
func appServerPath(slug string) string { return "/mcp/app/" + slug + "/" }

// handleApp serves one app's MCP address: GET opens the event stream (or, in
// a browser, shows what the endpoint is), POST carries JSON-RPC.
func (T *MCPServer) handleApp(w http.ResponseWriter, r *http.Request) {
	slug := appSlugFromPath(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			T.handleSSE(w, r)
		} else {
			T.handleStatusPage(w, r)
		}
	case http.MethodPost:
		T.handleAppRPC(w, r, slug)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// keyApp is the app slug as the request's key reaches it, with why not when it
// does not: no key, a key refused here, or an app this key does not reach.
func (T *MCPServer) keyApp(r *http.Request, slug, action string) (*AccountToken, customapps.OfferingApp, string) {
	owner, refusal, _ := T.authorize(r, action)
	if refusal != "" {
		return nil, customapps.OfferingApp{}, refusal
	}
	tok := AccountTokenFromRequest(r)
	if tok == nil {
		return nil, customapps.OfferingApp{}, "Unauthorized: this endpoint needs an MCP key in the X-API-Key header (Account > API keys, or the app's Settings)."
	}
	if !keyReachesApp(tok, slug) {
		Log("[mcpserver] %s on app %q REJECTED: key %q does not reach it (owner=%s)", action, slug, tok.Name, owner)
		return nil, customapps.OfferingApp{}, "Forbidden: this key does not reach this app."
	}
	for _, a := range reachedApps(tok, slug) {
		return tok, a, ""
	}
	return nil, customapps.OfferingApp{}, "Not found: you have no app here at this address that offers anything over MCP (it may be switched off, or offer nothing beyond its page)."
}

// appInstructions is what the handshake tells a client about the app.
func appInstructions(a customapps.OfferingApp) string {
	s := a.Name + ", an app on gohort."
	if a.Desc != "" {
		s += " " + strings.TrimSuffix(a.Desc, ".") + "."
	}
	return s + " Each tool is something the app offers beyond its page; calling one is the same as its owner clicking it there."
}

// handleAppRPC answers the JSON-RPC of one app's server.
func (T *MCPServer) handleAppRPC(w http.ResponseWriter, r *http.Request, slug string) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Log("[mcpserver] app %q: bad request: %v", slug, err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		// Open, as the general endpoint's is, so a key problem shows on the
		// first call rather than as a dead connection. Only a key that
		// reaches the app learns its name.
		pv := "2024-11-05"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			pv = p.ProtocolVersion
		}
		result := map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gohort app", "version": AppVersion},
		}
		if _, a, refusal := T.keyApp(r, slug, "initialize"); refusal == "" {
			result["serverInfo"] = map[string]any{"name": a.Name, "version": AppVersion}
			result["instructions"] = appInstructions(a)
		}
		resp.Result = result
		Log("[mcpserver] app %q: initialize (protocol=%s)", slug, pv)
	case "tools/list":
		defs := []map[string]any{}
		if tok, _, refusal := T.keyApp(r, slug, "tools/list"); refusal == "" {
			defs = append(defs, allowedToolDefs(appToolDefs(tok, slug), tok)...)
		}
		resp.Result = map[string]any{"tools": defs}
		Log("[mcpserver] app %q: tools/list -> %d tools", slug, len(defs))
	case "tools/call":
		tok, a, refusal := T.keyApp(r, slug, "tools/call")
		if refusal != "" {
			resp.Result = toolText(refusal, true)
			break
		}
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Result = toolText("error: bad params: "+err.Error(), true)
			break
		}
		if !tok.AllowsTool(toolScope(p.Name)) {
			resp.Result = toolText(fmt.Sprintf("error: this key may not call %q: enable it on the key under Account > API keys > Access", p.Name), true)
			break
		}
		text, handled, err := callAppTool(r.Context(), tok, slug, p.Name, p.Arguments)
		switch {
		case !handled:
			var have []string
			for _, d := range appToolDefs(tok, slug) {
				if n, _ := d["name"].(string); n != "" {
					have = append(have, n)
				}
			}
			resp.Result = toolText(fmt.Sprintf("error: %s offers no tool %q; it offers: %s", a.Name, p.Name, strings.Join(have, ", ")), true)
		case err != nil:
			Log("[mcpserver] app %q: tools/call %s error (owner=%s): %v", slug, p.Name, tok.Owner, err)
			resp.Result = toolText("error: "+err.Error(), true)
		default:
			Log("[mcpserver] app %q: tools/call %s ok (owner=%s, %d chars)", slug, p.Name, tok.Owner, len(text))
			resp.Result = toolResult(text, nil)
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
