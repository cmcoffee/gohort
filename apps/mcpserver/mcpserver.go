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
	T.HandleFunc("/", T.handleRPC)
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
