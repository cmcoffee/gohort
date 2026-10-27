package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A connector is a declaring consumer of a credential, exactly as a tool
// with fetch_via: is.
//
// The admin's "orphaned" check only knew about tools, so a secured
// credential reached only through a connector was badged unreachable
// while working perfectly. That went unnoticed while nothing important
// was secured-and-connector-only; securing the peer key made it the
// first case, and the local image and messaging connectors have the same
// shape.

func TestConnectorsUsingCredentialFindsEveryKind(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// Written straight to the store rather than through SaveConnector:
	// that validates each kind's full spec, and what is under test is the
	// probe over whatever specs are already there — including ones an
	// older build wrote.
	save := func(name string, spec any) {
		raw, _ := json.Marshal(spec)
		db.Set(connectorsTable, name, Connector{Name: name, Kind: "rest_image", Spec: raw})
	}
	// Two kinds of spec, both using the shared "credential" key — which
	// is why the probe decodes that key rather than switching over
	// connector kinds, a switch that goes stale the first time somebody
	// adds one.
	save("peer-gpu-comfy", map[string]any{"credential": "peer_gpu_key", "base_url": "https://gpu"})
	save("peer-gpu-flux", map[string]any{"credential": "peer_gpu_key"})
	save("local-comfy", map[string]any{"credential": "local_key"})
	save("no-cred", map[string]any{"base_url": "https://x"})

	got := ConnectorsUsingCredential(db, "peer_gpu_key")
	if len(got) != 2 || got[0] != "peer-gpu-comfy" || got[1] != "peer-gpu-flux" {
		t.Fatalf("expected both peer backends, sorted: %v", got)
	}
	if one := ConnectorsUsingCredential(db, "local_key"); len(one) != 1 {
		t.Errorf("local: %v", one)
	}
	// Case-insensitive, because credential names are compared that way
	// everywhere else.
	if up := ConnectorsUsingCredential(db, "PEER_GPU_KEY"); len(up) != 2 {
		t.Errorf("case should not matter: %v", up)
	}
	// A credential nothing references is what "orphaned" actually means.
	if none := ConnectorsUsingCredential(db, "unused"); len(none) != 0 {
		t.Errorf("expected nothing: %v", none)
	}
	// And the degenerate inputs, since this feeds a badge rather than a
	// decision: no database, no name, a spec that is not an object.
	if ConnectorsUsingCredential(nil, "x") != nil {
		t.Error("no database should answer nothing")
	}
	if ConnectorsUsingCredential(db, "  ") != nil {
		t.Error("no name should answer nothing")
	}
	db.Set(connectorsTable, "junk",
		Connector{Name: "junk", Kind: "rest_image", Spec: json.RawMessage(`"a string"`)})
	if got := ConnectorsUsingCredential(db, "peer_gpu_key"); len(got) != 2 {
		t.Errorf("an undecodable spec should be skipped, not fatal: %v", got)
	}
}

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
