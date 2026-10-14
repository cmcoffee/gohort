package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMCPServer answers the MCP handshake and a tool list, or refuses with 401
// when signIn is set, as a server that signs in per person does.
func fakeMCPServer(t *testing.T, signIn bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signIn {
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.ID == 0 { // a notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "Calendar", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "list_events", "description": "List events.", "inputSchema": map[string]any{"type": "object"}},
				{"name": "create_event", "description": "Create an event.", "inputSchema": map[string]any{"type": "object"}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func mcpConnector(url, auth string) Connector {
	spec, _ := json.Marshal(RemoteMCPSpec{URL: url, AuthMode: auth})
	return Connector{Name: "calendar", Kind: RemoteMCPConnectorKind, Spec: spec}
}

// A remote MCP connector's test connects to the server for real: the
// handshake and its tool list. Before, it read the config only and passed a
// connector whose server was down.
func TestARemoteMCPConnectorTestConnects(t *testing.T) {
	out, probed, err := ProbeConnector(context.Background(), mcpConnector(fakeMCPServer(t, false), ""))
	if err != nil || !probed || !strings.Contains(out, "Calendar") || !strings.Contains(out, "2 tool(s)") {
		t.Fatalf("probe: %q probed=%v err=%v", out, probed, err)
	}
	// A server that is not there fails the test.
	if _, _, err := ProbeConnector(context.Background(), mcpConnector("http://127.0.0.1:1/mcp", "")); err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Errorf("an unreachable server passed: %v", err)
	}
}

// A server that signs in per person refuses a check with nobody signed in:
// reaching it and being asked to sign in is the expected answer for an oauth
// connector, and a failure for one that expected no sign-in.
func TestAPerPersonMCPServerIsReachedNotFailed(t *testing.T) {
	url := fakeMCPServer(t, true)
	out, _, err := ProbeConnector(context.Background(), mcpConnector(url, string(MCPAuthOAuth)))
	if err != nil || !strings.Contains(out, "asks for sign-in") {
		t.Errorf("oauth: %q %v", out, err)
	}
	if _, _, err := ProbeConnector(context.Background(), mcpConnector(url, "")); err == nil || !strings.Contains(err.Error(), "requires sign-in") {
		t.Errorf("no auth, refused: %v", err)
	}
}

// A kind with no live check says so: probed is false, and the test reports
// that it checked the configuration only.
func TestAKindWithNoLiveCheckIsNotProbed(t *testing.T) {
	RegisterConnectorKind("probe_test_kind", noProbeHandler{})
	if _, probed, err := ProbeConnector(context.Background(), Connector{Name: "x", Kind: "probe_test_kind"}); probed || err != nil {
		t.Errorf("probed=%v err=%v for a kind with no live check", probed, err)
	}
}

type noProbeHandler struct{}

func (noProbeHandler) Validate(Connector) error    { return nil }
func (noProbeHandler) Materialize(Connector) error { return nil }
func (noProbeHandler) Teardown(Connector) error    { return nil }
func (noProbeHandler) Summary(Connector) string    { return "" }
