package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubMCP is a minimal MCP server over Streamable HTTP for tests. It
// answers initialize / tools/list / tools/call and can reply in either
// application/json or text/event-stream mode. It asserts the session id
// is echoed after initialize and (optionally) a bearer token.
type stubMCP struct {
	sse         bool
	wantBearer  string
	sawSession  bool // a non-initialize request carried the session id
	sessionID   string
	gotBearerOK bool
}

func (s *stubMCP) handler() http.HandlerFunc {
	s.sessionID = "sess-123"
	return func(w http.ResponseWriter, r *http.Request) {
		if s.wantBearer != "" && r.Header.Get("Authorization") == "Bearer "+s.wantBearer {
			s.gotBearerOK = true
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Notifications (no id) get 202 and no body.
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") == s.sessionID {
			s.sawSession = true
		}

		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", s.sessionID)
			result = map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "stub", "version": "9"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search the wiki",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"query": map[string]any{"type": "string", "description": "the query"}},
					"required":   []string{"query"},
				},
			}}}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(mustJSON(req.Params), &p)
			result = map[string]any{"content": []map[string]any{
				{"type": "text", "text": "hit for " + fmt.Sprint(p.Arguments["query"])},
			}}
		default:
			result = map[string]any{}
		}

		respBytes := mustJSON(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(result)})
		if s.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, ": keep-alive\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", respBytes)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func runClientFlow(t *testing.T, stub *stubMCP, auth Authorizer) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := New(NewHTTPTransport(srv.URL, HTTPOptions{Auth: auth}))
	defer c.Close()
	ctx := context.Background()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if info := c.ServerInfo(); info.ServerName != "stub" {
		t.Fatalf("serverInfo name = %q, want stub", info.ServerName)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools = %+v, want one named search", tools)
	}
	if req, _ := tools[0].InputSchema["required"].([]any); len(req) != 1 {
		t.Fatalf("search required = %v, want [query]", tools[0].InputSchema["required"])
	}

	out, err := c.CallTool(ctx, "search", map[string]any{"query": "widgets"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if out != "hit for widgets" {
		t.Fatalf("call result = %q, want 'hit for widgets'", out)
	}

	if !stub.sawSession {
		t.Errorf("server never saw the Mcp-Session-Id echoed after initialize")
	}
}

func TestClientJSONMode(t *testing.T) {
	runClientFlow(t, &stubMCP{sse: false}, nil)
}

func TestClientSSEMode(t *testing.T) {
	runClientFlow(t, &stubMCP{sse: true}, nil)
}

func TestClientBearerAuth(t *testing.T) {
	stub := &stubMCP{sse: false, wantBearer: "tok-abc"}
	auth := func(req *http.Request) error {
		req.Header.Set("Authorization", "Bearer tok-abc")
		return nil
	}
	runClientFlow(t, stub, auth)
	if !stub.gotBearerOK {
		t.Errorf("server never received the expected bearer token")
	}
}

func TestCallToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		if req.Method == "tools/call" {
			result = map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": "boom"}},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(mustJSON(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(result)}))
	}))
	defer srv.Close()

	c := New(NewHTTPTransport(srv.URL, HTTPOptions{}))
	defer c.Close()
	if _, err := c.CallTool(context.Background(), "x", nil); err == nil {
		t.Fatal("expected error from isError result, got nil")
	}
}

func TestRPCErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Write(mustJSON(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32601, Message: "no method"}}))
	}))
	defer srv.Close()

	c := New(NewHTTPTransport(srv.URL, HTTPOptions{}))
	defer c.Close()
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected rpc error, got nil")
	}
}
