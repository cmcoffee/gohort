package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// protocolVersion is the MCP revision we advertise on initialize.
// 2025-06-18 is the current spec; servers that only support older
// revisions still answer (the server echoes its own version in the
// initialize result — we don't hard-fail on a mismatch).
const protocolVersion = "2025-06-18"

// --- JSON-RPC 2.0 wire types ---

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"` // omitted for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object surfaced to callers.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp rpc %d: %s", e.Code, e.Message) }

// --- MCP payload types ---

// ToolDef is one tool as returned by tools/list. InputSchema is the
// raw JSON Schema object; the adapter flattens it into core.ToolParam.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is a logical MCP session over a Transport. It is safe for
// concurrent use: id assignment is atomic and the transport handles its
// own write serialization.
type Client struct {
	t      Transport
	nextID int64

	mu          sync.RWMutex
	initialized bool
	serverInfo  ServerInfo
}

// ServerInfo captures the handshake result for diagnostics.
type ServerInfo struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerName      string
	ServerVersion   string
}

// New wraps a Transport in a Client. The caller owns the transport's
// lifetime (Close it when done) unless they call Client.Close.
func New(t Transport) *Client { return &Client{t: t} }

// Close shuts the underlying transport down.
func (c *Client) Close() error { return c.t.Close() }

// ServerInfo returns the negotiated handshake info (zero value until
// Initialize succeeds).
func (c *Client) ServerInfo() ServerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverInfo
}

// call sends a request and decodes the response result, surfacing any
// JSON-RPC error.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	frame, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	respFrame, err := c.t.Call(ctx, frame)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", method, err)
	}
	var resp rpcResponse
	if err := json.Unmarshal(respFrame, &resp); err != nil {
		return nil, fmt.Errorf("mcp %s: bad response frame: %w", method, err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	// id mismatch is a server bug; tolerate but don't trust silently.
	if resp.ID != id {
		return nil, fmt.Errorf("mcp %s: response id %d != request id %d", method, resp.ID, id)
	}
	return resp.Result, nil
}

// Initialize performs the MCP handshake: initialize request followed by
// the notifications/initialized notification. Must be called before
// ListTools / CallTool.
func (c *Client) Initialize(ctx context.Context) error {
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "gohort", "version": "1"},
	})
	if err != nil {
		return err
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	_ = json.Unmarshal(raw, &res) // best-effort; absence is non-fatal

	notif, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized", Params: map[string]any{}})
	if err != nil {
		return err
	}
	if err := c.t.Notify(ctx, notif); err != nil {
		return fmt.Errorf("mcp notifications/initialized: %w", err)
	}

	c.mu.Lock()
	c.initialized = true
	c.serverInfo = ServerInfo{
		ProtocolVersion: res.ProtocolVersion,
		ServerName:      res.ServerInfo.Name,
		ServerVersion:   res.ServerInfo.Version,
	}
	c.mu.Unlock()
	return nil
}

// ListTools enumerates the server's tools.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// callResult mirrors the MCP tools/call result.
type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// CallTool invokes a tool by its raw MCP name and returns the
// concatenated text content. A tool that reports isError surfaces as a
// Go error carrying the text.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var r callResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, blk := range r.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
			continue
		}
		b.WriteString("[" + blk.Type + " content omitted]")
	}
	if r.IsError {
		return "", fmt.Errorf("tool reported error: %s", b.String())
	}
	return b.String(), nil
}
