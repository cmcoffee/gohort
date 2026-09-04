// Package mcpserver exposes gohort's agents to an external MCP client
// (Claude Desktop) over a minimal JSON-RPC endpoint. It is the inverse of
// core/mcp_manager.go: that dials OUT to remote MCP servers; this lets a
// remote MCP client call IN and drive a gohort agent.
//
// Why this exists: Claude Desktop is a stateless client with no daemon, so it
// cannot do "every morning at 8". gohort is a persistent server that already
// schedules (standing agents). This bridges the gap: Claude asks the agent to
// set something up, gohort owns the durable execution and delivery.
//
// Auth reuses the bridge key (X-API-Key -> owner) so there is nothing new to
// mint. Dispatch reuses core.RunChannelAgent, which is synchronous and returns
// the agent's reply: a perfect fit for an MCP tools/call round trip.
//
// Transport note: this speaks JSON-RPC over a single POST, not the full MCP
// Streamable-HTTP spec (no SSE channel, no session ids). The local stdio shim
// is what makes Claude Desktop happy, and for a single local user that is
// enough. Grow to full Streamable HTTP only for a remote connector.
//
// Not enabled by default. Turn it on with a blank import in agents.go:
//
//	_ "github.com/cmcoffee/gohort/apps/mcpserver"
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterApp(new(MCPServer))
	// Declare the MCP endpoint as an admin-gateable, per-key feature — the
	// same two-tier control the /v1 endpoint has. Without this, a personal
	// access token the user scoped to NOTHING (or to openai only) still
	// dispatched agents over MCP: the endpoint authenticated the key but
	// never consulted its scope. Admin gate defaults open (live feature,
	// non-breaking); the per-key gate honors AccountToken.Scope.Features
	// with the usual nil-scope legacy grandfather.
	RegisterShareableFeature(ShareableFeature{
		Key:   MCPFeatureKey,
		Label: "MCP endpoint (external clients)",
		Desc:  "Let a user's personal access tokens dispatch their MCP-exposed agents from an external MCP client (e.g. Claude Desktop).",
	})
}

// MCPFeatureKey gates the /mcp/ endpoint per admin policy and per key scope.
const MCPFeatureKey = "mcp"

// defaultAgent is where an un-targeted ask lands. seed-chat absorbed the
// Operator, so it carries the scheduling tools (create_standing_agent, etc).
const defaultAgent = "seed-chat"

// mcpSession is the single rolling thread Claude Desktop talks to. It is
// owner-scoped by RunChannelAgent (runs under the resolved user's store), so
// one constant is fine for a single-user deployment.
const mcpSession = "mcp-desktop"

type MCPServer struct {
	AppCore
}

// --- core.Agent interface (dashboard-only app) -------------------------------

func (T MCPServer) Name() string         { return "mcpserver" }
func (T MCPServer) SystemPrompt() string { return "" }
func (T MCPServer) Desc() string {
	return "Apps: MCP server - expose gohort agents to an external MCP client."
}
func (T *MCPServer) Init() error { return T.Flags.Parse() }
func (T *MCPServer) Main() error {
	Log("mcpserver is dashboard/endpoint-only. Start with: gohort serve")
	return nil
}

// --- core.WebApp (SimpleWebApp) ----------------------------------------------

func (T *MCPServer) WebPath() string { return "/mcp" }
func (T *MCPServer) WebName() string { return "MCP Server" }
func (T *MCPServer) WebDesc() string { return "External MCP client bridge." }

// WebHidden keeps the /mcp/ endpoint mounted but drops the dashboard tile —
// the human-facing info now lives as a section in Bridges (where its auth
// bridge keys are managed), so a standalone tile would be redundant. The
// status page at /mcp/ still renders for anyone who navigates there directly.
func (T *MCPServer) WebHidden() bool { return true }

func (T *MCPServer) Routes() {
	// Public path: auth is the X-API-Key header, NOT a dashboard cookie, so it
	// must bypass AuthMiddleware. Mirrors how bridges registers /api/hook.
	RegisterPublicPath("/mcp/")
	T.HandleFunc("/", T.handle)
	T.HandleFunc("/status", T.handleStatus) // JSON for the status page (auth'd in-handler)
}

// handle dispatches by method. MCP's Streamable HTTP transport opens the
// server→client channel with a GET (an SSE stream) and sends JSON-RPC with
// POST; a POST-only endpoint 405s the GET and the client never connects.
func (T *MCPServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// An MCP client opens the server→client channel with
		// Accept: text/event-stream; a browser hitting the dashboard tile
		// sends text/html. Stream to the former, show a status page to the
		// latter (so clicking the tile isn't a hung event-stream).
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			T.handleSSE(w, r)
		} else {
			T.handleStatusPage(w, r)
		}
	case http.MethodPost:
		T.handleRPC(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStatusPage renders the human-facing dashboard view: what the endpoint
// is, how to connect, and what it exposes. Auth'd as a normal dashboard page
// (the protocol GET/POST stay open; only this human view requires login).
func (T *MCPServer) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	ui.Page{
		Title:     "MCP Server",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "800px",
		Sections: []ui.Section{{
			Title:    "Inbound MCP endpoint",
			Subtitle: "Point an MCP client (Streamable HTTP transport) at the endpoint below and authenticate with a personal access token (create one on your Account page) in the X-API-Key header. The client can then dispatch to your agents via tools/call.",
			Body: ui.DisplayPanel{
				Source: "status",
				Pairs: []ui.DisplayPair{
					{Label: "Endpoint", Field: "endpoint", Mono: true},
					{Label: "Transport", Field: "transport"},
					{Label: "Auth", Field: "auth"},
					{Label: "Exposed tools", Field: "tools"},
					{Label: "Default agent", Field: "agent", Mono: true},
				},
			},
		}},
	}.ServeHTTP(w, r)
}

// handleStatus is the JSON feed behind the status page's DisplayPanel.
func (T *MCPServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	names := make([]string, 0)
	for _, d := range toolDefs() {
		if n, ok := d["name"].(string); ok {
			names = append(names, n)
		}
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"endpoint":  scheme + "://" + r.Host + "/mcp/",
		"transport": "Streamable HTTP (GET opens an SSE stream, POST carries JSON-RPC)",
		"auth":      "X-API-Key header — a personal access token (create one on your Account page: /account)",
		"tools":     strings.Join(names, ", "),
		"agent":     defaultAgent,
	})
}

// handleSSE serves the GET server→client stream. We have no server-initiated
// messages to push (every JSON-RPC response rides its POST), so this just
// opens text/event-stream and holds the connection open with heartbeats until
// the client disconnects — which is what a Streamable HTTP client needs to
// consider itself connected. Open (no auth): no data flows here; tools/call
// is the gated action.
func (T *MCPServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	Log("[mcpserver] SSE stream opened from %s", r.RemoteAddr)
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			Log("[mcpserver] SSE stream closed (%s)", r.RemoteAddr)
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

// --- JSON-RPC plumbing -------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent on notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (T *MCPServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Log("[mcpserver] bad request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Notifications (no id) get no response body.
	if len(req.ID) == 0 {
		Log("[mcpserver] notification %q", req.Method)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		// The handshake is OPEN — no auth — so a key problem can't kill the
		// connection before it starts (only tools/call, the action, is gated
		// below). Echo the client's protocol version when it sends one, for
		// maximum compatibility; fall back to a known-good version.
		pv := "2024-11-05"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			pv = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gohort", "version": AppVersion},
		}
		Log("[mcpserver] initialize (protocol=%s)", pv)
	case "tools/list":
		// Filtered by the calling key. A client shown a tool its own key will
		// refuse learns the refusal by calling it, which reads as the server
		// being broken rather than the key being narrow. Unauthenticated
		// listing keeps the full set: initialize/list are how a client decides
		// whether to bother authenticating at all.
		defs := toolDefs()
		// Say WHY the list is the size it is. "N tools" alone cannot tell a
		// client that cached an old list from a key narrowed to fewer, and
		// those need opposite fixes — reconnect the client, or retick the
		// scope. Names, because the question is always about one tool.
		scopeNote := "unauthenticated request — full list"
		if tok := AccountTokenFromRequest(r); tok != nil {
			before := len(defs)
			defs = allowedToolDefs(defs, tok)
			switch {
			case tok.Scope == nil:
				scopeNote = "key predates scoping — full list"
			case tok.Scope.Tools == nil:
				scopeNote = "key has no tool list — not narrowed, full list"
			default:
				scopeNote = fmt.Sprintf("key narrowed to %d tool(s): %s", len(*tok.Scope.Tools), strings.Join(*tok.Scope.Tools, ", "))
			}
			if len(defs) != before {
				scopeNote += fmt.Sprintf(" — %d of %d hidden", before-len(defs), before)
			}
		}
		names := make([]string, 0, len(defs))
		for _, d := range defs {
			if n, _ := d["name"].(string); n != "" {
				names = append(names, n)
			}
		}
		resp.Result = map[string]any{"tools": defs}
		Log("[mcpserver] tools/list -> %d tools [%s] (%s)", len(defs), strings.Join(names, ", "), scopeNote)
	case "tools/call":
		// Only the ACTION needs auth. Resolve the bridge key -> owner here and
		// return a CLEAR JSON-RPC tool error (not an opaque HTTP 401 the client
		// won't surface) when it's missing/unrecognized.
		owner := DesktopBridgeUserOf(r)
		if owner == "" {
			Log("[mcpserver] tools/call REJECTED — no valid X-API-Key (mint a bridge key in Bridges admin)")
			resp.Result = toolText("Unauthorized: this endpoint needs a valid gohort personal access token in the X-API-Key header. Create one on your Account page (/account) and put it in the connector config.", true)
			break
		}
		// Feature gates, mirroring the /v1 endpoint's two tiers:
		//   1. admin — may this USER use the MCP endpoint at all
		//   2. key   — did the user enable "mcp" on THIS key (nil scope =
		//      legacy unscoped key, grandfathered by AllowsFeature)
		// A session-cookie or bridge-key request has no account token and
		// skips tier 2 — those are the user themselves / an admin-minted
		// bridge key, not a scoped personal token.
		if !FeatureAllowedForUser(T.DB, MCPFeatureKey, owner) {
			Log("[mcpserver] tools/call REJECTED — admin policy denies MCP for user=%s", owner)
			resp.Result = toolText("Forbidden: an admin has not enabled MCP access for your account (Admin > Feature Access).", true)
			break
		}
		if tok := AccountTokenFromRequest(r); tok != nil && !tok.AllowsFeature(MCPFeatureKey) {
			Log("[mcpserver] tools/call REJECTED — key %q lacks the mcp feature (owner=%s)", tok.Name, owner)
			resp.Result = toolText("Forbidden: this access token is not allowed to use the MCP endpoint. On your Account page, open this key's Configure access and enable \"MCP endpoint\".", true)
			break
		}
		text, images, err := T.callTool(r.Context(), owner, AccountTokenFromRequest(r), req.Params)
		if err != nil {
			Log("[mcpserver] tools/call error (owner=%s): %v", owner, err)
			resp.Result = toolText("error: "+err.Error(), true)
		} else {
			Log("[mcpserver] tools/call ok (owner=%s, %d chars, %d image(s))", owner, len(text), len(images))
			resp.Result = toolResult(text, images)
		}
	default:
		Log("[mcpserver] unknown method %q", req.Method)
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// toolText wraps a string in MCP's content envelope.
func toolText(s string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": s}},
		"isError": isErr,
	}
}

// maxMCPImages bounds how many pictures ride back on one call. A turn that
// produced a contact sheet should not put twenty multi-megabyte blocks through
// a JSON-RPC response; the text says how many were held back.
const maxMCPImages = 4

// maxMCPImageBytes is the per-image ceiling, decoded. Base64 inflates by a
// third and the whole response is one JSON document, so a very large render is
// named in the text rather than sent.
const maxMCPImageBytes = 8 << 20

// toolResult wraps text PLUS any images the turn produced. An agent that
// generates a picture and hands it back had nowhere to put it before: the
// envelope was text-only, so the reply arrived describing an image that never
// travelled. MCP content blocks are the channel; this fills them.
func toolResult(text string, images []string) map[string]any {
	content := []map[string]any{{"type": "text", "text": text}}
	sent, skipped := 0, 0
	for _, b64 := range images {
		if sent >= maxMCPImages {
			skipped++
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) == 0 || len(raw) > maxMCPImageBytes {
			skipped++
			continue
		}
		content = append(content, map[string]any{
			"type":     "image",
			"data":     b64,
			"mimeType": http.DetectContentType(raw),
		})
		sent++
	}
	if skipped > 0 {
		// Say what did not come. A silent drop reads as the agent claiming an
		// image it never sent, which is the failure this whole channel exists
		// to avoid.
		content[0]["text"] = fmt.Sprintf("%s\n\n[%d image(s) attached; %d not included (too large, or past the %d-image limit for one reply) — they remain in the agent's workspace and can be re-sent]",
			text, sent, skipped, maxMCPImages)
	}
	return map[string]any{"content": content, "isError": false}
}

// --- the two tools -----------------------------------------------------------

func init() {
	// Published for the per-key scope picker on the account page. Registered
	// from here because core must not know these names — it owns the registry,
	// this package owns the tools.
	RegisterMCPBuiltinTool("ask_agent", "Ask an agent")
	RegisterMCPBuiltinTool("list_agents", "List agents")
	RegisterMCPBuiltinTool("recent_results", "Recent background results")
}

func toolDefs() []map[string]any {
	defs := []map[string]any{
		{
			"name":        "ask_agent",
			"description": "Send a message to a gohort agent and get its reply. The agent has persistent memory, scheduling (it can set up recurring tasks that run on gohort's server), and delivery channels. To schedule something, just ask in plain language, e.g. 'every weekday at 8am, summarize my calendar and text it to me'. Pass times exactly as the user said them; do NOT convert to UTC.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "What to ask the agent."},
					"agent":   map[string]any{"type": "string", "description": "Agent id (optional; defaults to the main agent)."},
				},
				"required": []string{"message"},
			},
		},
		{
			"name":        "list_agents",
			"description": "List the gohort agents you can send messages to, with what each one is for. Call this before ask_agent when you don't already know which agent to use, or when the user names an agent you haven't seen — the `id` on each row is what ask_agent's `agent` argument takes. Only agents the account has made reachable from outside appear here.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "recent_results",
			"description": "List recent results from gohort's scheduled and background runs, newest first. Use this to report back on what scheduled tasks have produced since you last checked.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"since_hours": map[string]any{"type": "integer", "description": "Only runs in the last N hours (optional)."},
					"limit":       map[string]any{"type": "integer", "description": "Max rows (optional, default 20)."},
				},
			},
		},
	}
	// App-contributed tools (apps/guides etc. register via core.RegisterMCPTool):
	// the MCP server stays domain-agnostic and just surfaces whatever apps add.
	// Each is gated by the admin exposure policy (default off) so the external
	// surface is opt-in.
	for _, s := range RegisteredMCPTools() {
		if !MCPAppToolExposed(s.Name) {
			continue
		}
		schema := s.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		defs = append(defs, map[string]any{
			"name":        s.Name,
			"description": s.Description,
			"inputSchema": schema,
		})
	}
	return defs
}

// allowedToolDefs keeps only the tools this key may call.
func allowedToolDefs(defs []map[string]any, tok *AccountToken) []map[string]any {
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		name, _ := d["name"].(string)
		if tok.AllowsTool(name) {
			out = append(out, d)
		}
	}
	return out
}

func (T *MCPServer) callTool(ctx context.Context, owner string, token *AccountToken, raw json.RawMessage) (string, []string, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", nil, fmt.Errorf("bad params: %w", err)
	}
	// Enforced here as well as in the listing, because a client can call a name
	// it learned somewhere else — a filtered list is a courtesy, not a gate.
	if !token.AllowsTool(p.Name) {
		return "", nil, fmt.Errorf("this key may not call %q — enable it on the key under Account → API keys → Configure access", p.Name)
	}
	switch p.Name {
	case "ask_agent":
		return T.askAgent(ctx, owner, token, p.Arguments)
	case "list_agents":
		text, err := T.listAgents(owner, token)
		return text, nil, err
	case "recent_results":
		text, err := T.recentResults(owner, p.Arguments)
		return text, nil, err
	default:
		// App-contributed tools registered via core.RegisterMCPTool. They run
		// scoped to the bridge-key owner, like the built-ins above — but only when
		// the admin has exposed them (defense in depth alongside the tools/list
		// filter, since a client could call a name it learned elsewhere).
		if spec, ok := LookupMCPTool(p.Name); ok {
			if !MCPAppToolExposed(p.Name) {
				return "", nil, fmt.Errorf("tool %q is not exposed over MCP — enable it in Admin → MCP Tools", p.Name)
			}
			text, err := spec.Handler(ctx, owner, p.Arguments)
			return text, nil, err
		}
		// Name what IS here. An agent's own tools (image, web_search, …) are not
		// MCP tools — they belong to the agent and are reached by ASKING it — so
		// a bare "unknown tool" leaves a caller guessing whether it typed the
		// name wrong or the server is broken.
		var have []string
		for _, d := range toolDefs() {
			if n, _ := d["name"].(string); n != "" {
				have = append(have, n)
			}
		}
		return "", nil, fmt.Errorf("unknown tool %q — this server exposes: %s. An agent's own tools (image, web_search, …) are not callable here; ask the agent to use them via ask_agent", p.Name, strings.Join(have, ", "))
	}
}

// listAgents answers "which agent should I ask?" — the question ask_agent's
// schema poses and, until now, gave no way to answer. The set is the one the
// resolver would accept for this caller, so everything listed is dispatchable
// and everything dispatchable is listed.
func (T *MCPServer) listAgents(owner string, token *AccountToken) (string, error) {
	if ListExternalReachableAgentsFn == nil {
		return "", fmt.Errorf("agent listing not available (orchestrate not loaded)")
	}
	var granted func(string) bool
	if token != nil {
		granted = token.ExplicitTarget
	}
	agents := ListExternalReachableAgentsFn(T.DB, owner, granted)
	if len(agents) == 0 {
		// Say which switch turns this on. An empty list otherwise reads as "you
		// have no agents", which is almost never what happened.
		return "No agents are reachable over MCP. Open the agent's editor in gohort → Access & visibility → turn on \"Reachable over MCP\", then reconnect this connector so it re-reads the list. Only your own agents appear here; an app's built-in agents (Servitor, Guides, …) are reached by app name, not listed.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d agent(s) you can ask:\n", len(agents))
	for _, a := range agents {
		// id FIRST on every row: it is the value ask_agent takes, and a list
		// whose items cannot be passed back to the tool that needs them sends
		// the caller guessing at names.
		fmt.Fprintf(&b, "\n- id: %s\n  name: %s\n", a.ID, a.Name)
		if d := strings.TrimSpace(a.Description); d != "" {
			fmt.Fprintf(&b, "  what it does: %s\n", d)
		}
		// Say when one is a sub-agent. It is dispatchable — its owner ticked
		// the toggle — but a caller should know it normally works through its
		// parent, so asking the parent may be the better route.
		if a.ParentID != "" {
			fmt.Fprintf(&b, "  note: sub-agent of %s — usually reached by asking that agent instead\n", a.ParentID)
		}
	}
	b.WriteString("\nPass one of these ids as ask_agent's `agent` argument. Omitting it uses the account's main agent.")
	return b.String(), nil
}

func (T *MCPServer) askAgent(ctx context.Context, owner string, token *AccountToken, args map[string]any) (string, []string, error) {
	msg, _ := args["message"].(string)
	if strings.TrimSpace(msg) == "" {
		return "", nil, fmt.Errorf("message is required")
	}
	agent, _ := args["agent"].(string)
	if strings.TrimSpace(agent) == "" {
		agent = defaultAgent
	}
	if !ChannelAgentRunnerReady() {
		return "", nil, fmt.Errorf("agent runner not ready (orchestrate not loaded)")
	}
	// Only reachable agents can be dispatched, so a bridge key can't reach every
	// agent. Fails closed. Resolution goes name-or-id → canonical ID so the
	// per-app gate below sees the real id: for USER agents reachable means the
	// MCPExposed toggle; for APP agents (Servitor, Guides, …) it means the admin
	// enabled the app under Feature Access — they have no editor page, so the
	// feature grant IS their exposure consent.
	if ResolveExternalAgentFn != nil {
		// An agent this key EXPLICITLY grants is reachable through it even
		// without the MCPExposed toggle — the grant on the access menu is the
		// consent (nil-scope legacy keys grant nothing extra here).
		var granted func(string) bool
		if token != nil {
			granted = token.ExplicitTarget
		}
		id, ok := ResolveExternalAgentFn(T.DB, owner, agent, granted)
		if !ok {
			return "", nil, fmt.Errorf("agent %q is not reachable over MCP — for your own agents, turn on \"Reachable over MCP\" (agent editor → Access & visibility); for an app's agents (Servitor, Guides, …), an admin enables the app under Feature Access", agent)
		}
		agent = id
	} else if !MCPAgentExposed(owner, agent) {
		return "", nil, fmt.Errorf("agent %q is not reachable over MCP — turn on \"Reachable over MCP\" in its settings (agent editor → Access & visibility)", agent)
	}
	// Per-APP feature gate: dispatching an app-owned agent (Servitor, Guides, …)
	// needs the app enabled for this user (admin) AND on this key (user scope).
	// No-op for ordinary agents; nil token (session/bridge-key auth) skips the
	// key tier, same as the endpoint-level mcp gate.
	if ok, msg := KeyAllowsAppAgent(T.DB, owner, token, agent); !ok {
		return "", nil, fmt.Errorf("%s", msg)
	}
	// Synchronous: blocks until the agent finishes, returns its reply. Exactly
	// the MCP tools/call contract. SenderName attributes the turn to the
	// external caller in the transcript.
	reply, err := RunChannelAgent(ctx, ChannelInbound{
		Owner:      owner,
		AgentID:    agent,
		SessionID:  mcpSession,
		SenderName: "Claude Desktop",
		Text:       msg,
	})
	if err != nil {
		return "", nil, err
	}
	// Videos have no content type every client renders, so they are named
	// rather than sent — better than a block the other side drops silently.
	text := reply.Text
	if n := len(reply.Videos); n > 0 {
		text += fmt.Sprintf("\n\n[%d video(s) produced; this connector carries images only. Ask the agent to deliver them over a messaging channel, or open the thread in gohort.]", n)
	}
	return text, reply.Images, nil
}

func (T *MCPServer) recentResults(owner string, args map[string]any) (string, error) {
	f := RunFilter{Limit: 20}
	if n, ok := args["limit"].(float64); ok && n > 0 {
		f.Limit = int(n)
	}
	if h, ok := args["since_hours"].(float64); ok && h > 0 {
		f.Since = time.Now().Add(-time.Duration(h) * time.Hour)
	}
	runs := ListRuns(RootDB, owner, f)
	if len(runs) == 0 {
		return "No recent runs.", nil
	}
	var b strings.Builder
	for _, rr := range runs {
		// Name the schedule when the run came from one: otherwise every
		// recurring fire prints under its agent's label and the line cannot
		// say which task ran.
		label := rr.Agent
		if rr.Task != "" && rr.Task != rr.Agent {
			label = rr.Agent + " · " + rr.Task
		}
		fmt.Fprintf(&b, "[%s] %s (%s): %s\n",
			rr.Started.Local().Format("Jan 2 15:04"), label, rr.Status, rr.Summary)
	}
	return b.String(), nil
}
