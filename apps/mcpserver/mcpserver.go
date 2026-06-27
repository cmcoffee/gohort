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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(MCPServer)) }

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
			Subtitle: "Point an MCP client (Streamable HTTP transport) at the endpoint below and authenticate with a gohort bridge key in the X-API-Key header. The client can then dispatch to your agents via tools/call.",
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
		"auth":      "X-API-Key header — a gohort bridge key (mint one in Bridges admin)",
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
		resp.Result = map[string]any{"tools": toolDefs()}
		Log("[mcpserver] tools/list -> %d tools", len(toolDefs()))
	case "tools/call":
		// Only the ACTION needs auth. Resolve the bridge key -> owner here and
		// return a CLEAR JSON-RPC tool error (not an opaque HTTP 401 the client
		// won't surface) when it's missing/unrecognized.
		owner := DesktopBridgeUserOf(r)
		if owner == "" {
			Log("[mcpserver] tools/call REJECTED — no valid X-API-Key (mint a bridge key in Bridges admin)")
			resp.Result = toolText("Unauthorized: this endpoint needs a valid gohort bridge key in the X-API-Key header. Mint one in Bridges admin and put it in the connector config.", true)
			break
		}
		text, err := T.callTool(r.Context(), owner, req.Params)
		if err != nil {
			Log("[mcpserver] tools/call error (owner=%s): %v", owner, err)
			resp.Result = toolText("error: "+err.Error(), true)
		} else {
			Log("[mcpserver] tools/call ok (owner=%s, %d chars)", owner, len(text))
			resp.Result = toolText(text, false)
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

// --- the two tools -----------------------------------------------------------

func toolDefs() []map[string]any {
	return []map[string]any{
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
}

func (T *MCPServer) callTool(ctx context.Context, owner string, raw json.RawMessage) (string, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("bad params: %w", err)
	}
	switch p.Name {
	case "ask_agent":
		return T.askAgent(ctx, owner, p.Arguments)
	case "recent_results":
		return T.recentResults(owner, p.Arguments)
	default:
		return "", fmt.Errorf("unknown tool %q", p.Name)
	}
}

func (T *MCPServer) askAgent(ctx context.Context, owner string, args map[string]any) (string, error) {
	msg, _ := args["message"].(string)
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("message is required")
	}
	agent, _ := args["agent"].(string)
	if strings.TrimSpace(agent) == "" {
		agent = defaultAgent
	}
	if !ChannelAgentRunnerReady() {
		return "", fmt.Errorf("agent runner not ready (orchestrate not loaded)")
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
		return "", err
	}
	return reply.Text, nil
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
		fmt.Fprintf(&b, "[%s] %s (%s): %s\n",
			rr.Started.Local().Format("Jan 2 15:04"), rr.Agent, rr.Status, rr.Summary)
	}
	return b.String(), nil
}
