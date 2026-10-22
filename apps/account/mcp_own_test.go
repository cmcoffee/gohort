package account

// A person's own OAuth MCP server signs in from the same Connected accounts
// list, the same popup and the same callback as an administrator's.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/mcppersonal"
)

func TestOwnMCPServersSignInFromConnectedAccounts(t *testing.T) {
	T, sessions := accountFixture(t)
	if err := mcppersonal.Save(mcppersonal.Server{Owner: "alice", Name: "hosted", URL: "https://mcp.example.com/mcp", Auth: mcppersonal.AuthOAuth}, "", false); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	T.handleConnections(w, as(httptest.NewRequest(http.MethodGet, "/api/connections", nil), sessions["alice"]))
	var conns []PerUserConnection
	json.Unmarshal(w.Body.Bytes(), &conns)
	found := false
	for _, c := range conns {
		if c.Name == "hosted" && c.Kind == mcppersonal.ConnKind && !c.Connected && strings.Contains(c.ConnectURL, "own=1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("alice's server is not under Connected accounts: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	T.handleConnections(w, as(httptest.NewRequest(http.MethodGet, "/api/connections", nil), sessions["bob"]))
	if strings.Contains(w.Body.String(), "hosted") {
		t.Errorf("bob sees alice's server: %s", w.Body.String())
	}

	// Connect for one that is not theirs explains itself in the popup.
	w = httptest.NewRecorder()
	T.handleMCPConnect(w, as(httptest.NewRequest(http.MethodGet, "/mcp/connect?own=1&server=hosted", nil), sessions["bob"]))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Could not start signing in") {
		t.Errorf("bob's connect to alice's server: %d %s", w.Code, w.Body.String())
	}

	post := func(user string, body map[string]any) int {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		T.handleConnections(w, as(httptest.NewRequest(http.MethodPost, "/api/connections", bytes.NewReader(b)), sessions[user]))
		return w.Code
	}
	if code := post("alice", map[string]any{"name": "hosted", "kind": mcppersonal.ConnKind}); code != http.StatusBadRequest {
		t.Errorf("a key posted for an OAuth server: %d", code)
	}
	if code := post("alice", map[string]any{"name": "hosted", "kind": mcppersonal.ConnKind, "disconnect": true}); code != http.StatusNoContent {
		t.Errorf("disconnect: %d", code)
	}
}
