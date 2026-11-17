package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The key owner's exposed app tools are on tools/list beside the server's
// own, run on tools/call as the owner, and narrowed by the key like any tool.
// An action that asks to confirm first is not listed, and is refused if
// called: nobody here is asked over MCP.
func TestAnOwnersAppToolsAreOnMCP(t *testing.T) {
	prevAuth, prevRoot, prevApps := AuthDB, RootDB, AppToolsFor
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return root }
	RootDB = root
	ran := ""
	AppToolsFor = func(sess *ToolSession, user string) []AgentToolDef {
		if user != "user-a" {
			return nil
		}
		return []AgentToolDef{
			{Tool: Tool{Name: "books_add", Description: "Add a book.", Category: "App: Reading List",
				Parameters: map[string]ToolParam{"title": {Type: "string", Description: "The title"}}},
				Handler: func(_ context.Context, args map[string]any) (string, error) {
					ran = "added " + args["title"].(string)
					return `{"message":"added"}`, nil
				}},
			{Tool: Tool{Name: "books_wipe", Description: "Delete everything.", Category: "App: Reading List"},
				Confirmation: &ToolConfirmation{Prompt: "Delete every book?"},
				Handler:      func(context.Context, map[string]any) (string, error) { ran = "wiped"; return "", nil }},
		}
	}
	t.Cleanup(func() { AuthDB, RootDB, AppToolsFor = prevAuth, prevRoot, prevApps })

	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	rpc := func(key, method string, params any) map[string]any {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		r := httptest.NewRequest(http.MethodPost, "/mcp/", strings.NewReader(string(body)))
		r.Header.Set("X-API-Key", key)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.handleRPC(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	names := func(resp map[string]any) []string {
		var out []string
		res, _ := resp["result"].(map[string]any)
		tools, _ := res["tools"].([]any)
		for _, x := range tools {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m["name"].(string))
			}
		}
		return out
	}

	key := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}}).Token
	listed := names(rpc(key, "tools/list", map[string]any{}))
	if !contains(listed, "books_add") || contains(listed, "books_wipe") {
		t.Fatalf("listed: %v", listed)
	}
	resp := rpc(key, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "Dune"}})
	if ran != "added Dune" || resp["error"] != nil {
		t.Fatalf("the call: ran %q, %v", ran, resp)
	}
	ran = ""
	resp = rpc(key, "tools/call", map[string]any{"name": "books_wipe", "arguments": map[string]any{}})
	if ran != "" || !strings.Contains(mustJSON(resp), "asks to confirm") {
		t.Errorf("a confirm-first action over MCP: ran %q, %s", ran, mustJSON(resp))
	}

	narrowed := MintAccountTokenScoped("user-a", "narrow", &TokenScope{Features: []string{MCPFeatureKey}, Tools: &[]string{"ask_agent"}}).Token
	if contains(names(rpc(narrowed, "tools/list", map[string]any{})), "books_add") {
		t.Error("a key narrowed to ask_agent lists an app tool")
	}
	ran = ""
	rpc(narrowed, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "Dune"}})
	if ran != "" {
		t.Error("a key narrowed to ask_agent ran an app tool")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
