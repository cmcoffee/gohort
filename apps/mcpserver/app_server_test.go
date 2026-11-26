package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

func mcpKey(owner string, targets ...string) string {
	return MintAccountTokenScoped(owner, "client", &TokenScope{Features: []string{MCPFeatureKey}, Targets: targets}).Token
}

// An app's own address is a server of its own: the handshake names it and
// says what it is, but only to a key that reaches it; the list holds that
// app's tools alone (none that ask first, no built-ins, no other app's); a
// call runs the app's tool and refuses anything else.
func TestAnAppsAddressIsThatAppAlone(t *testing.T) {
	ran := ""
	root := appsFixture(t, &ran)
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	key := mcpKey("user-a")

	init := rpcTo(app, "/app/reading/", key, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	res, _ := init["result"].(map[string]any)
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "Reading List" || !strings.Contains(res["instructions"].(string), "Books to read.") || res["protocolVersion"] != "2025-03-26" {
		t.Errorf("the handshake names the app and says what it is: %s", mustJSON(init))
	}
	anon := rpcTo(app, "/app/reading/", "", "initialize", map[string]any{})
	res, _ = anon["result"].(map[string]any)
	if info, _ := res["serverInfo"].(map[string]any); info["name"] != "gohort app" || res["instructions"] != nil {
		t.Errorf("without a key the handshake names no app: %s", mustJSON(anon))
	}

	if got := toolNames(rpcTo(app, "/app/reading/", key, "tools/list", nil)); strings.Join(got, ",") != "books_add,reading_result" {
		t.Errorf("the app's address lists its own tools alone: %v", got)
	}
	if got := toolNames(rpcTo(app, "/app/reading/", "", "tools/list", nil)); len(got) != 0 {
		t.Errorf("without a key it lists nothing: %v", got)
	}

	resp := rpcTo(app, "/app/reading/", key, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "Dune"}})
	if ran != "added Dune" || strings.Contains(mustJSON(resp), `"isError":true`) {
		t.Fatalf("the call runs the app's tool: ran %q, %s", ran, mustJSON(resp))
	}
	for _, name := range []string{"pantry_list", "ask_agent", "list_agents", "books_wipe"} {
		ran = ""
		resp = rpcTo(app, "/app/reading/", key, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if ran != "" || !strings.Contains(mustJSON(resp), `"isError":true`) {
			t.Errorf("%s at the app's address: ran %q, %s", name, ran, mustJSON(resp))
		}
	}
	if !strings.Contains(mustJSON(rpcTo(app, "/app/reading/", key, "tools/call", map[string]any{"name": "pantry_list"})), "Reading List offers no tool") {
		t.Error("an unknown tool is answered with what the app offers")
	}

	// Someone else's key reaches no app of theirs by that slug.
	other := mcpKey("user-b")
	if got := toolNames(rpcTo(app, "/app/reading/", other, "tools/list", nil)); len(got) != 0 {
		t.Errorf("another person's key lists the app's tools: %v", got)
	}
	ran = ""
	resp = rpcTo(app, "/app/reading/", other, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "x"}})
	if ran != "" || !strings.Contains(mustJSON(resp), "Not found") {
		t.Errorf("another person's key ran the app's tool: ran %q, %s", ran, mustJSON(resp))
	}
	init = rpcTo(app, "/app/reading/", other, "initialize", map[string]any{})
	if strings.Contains(mustJSON(init), "Reading List") {
		t.Errorf("another person's key learns the app's name: %s", mustJSON(init))
	}
}

// A key made for an app reaches that app and nothing else: at the general
// address it lists and runs that app's tools alone, no agent and no built-in,
// and at another app's address it is refused. A key that names an app and an
// agent keeps the agent's tools, with that app alone; one naming no app
// reaches every app.
func TestAKeyMadeForAnAppReachesThatApp(t *testing.T) {
	ran := ""
	root := appsFixture(t, &ran)
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")

	only := mcpKey("user-a", customapps.AppToolsTarget+"reading")
	if got := toolNames(rpcTo(app, "/", only, "tools/list", nil)); strings.Join(got, ",") != "books_add,reading_result" {
		t.Errorf("a key for one app lists that app's tools alone: %v", got)
	}
	for _, name := range []string{"ask_agent", "list_agents", "recent_results", "pantry_list"} {
		ran = ""
		resp := rpcTo(app, "/", only, "tools/call", map[string]any{"name": name, "arguments": map[string]any{"message": "hi"}})
		if ran != "" || !strings.Contains(mustJSON(resp), "reaches nothing else here") {
			t.Errorf("a key for one app called %s: ran %q, %s", name, ran, mustJSON(resp))
		}
	}
	rpcTo(app, "/", only, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "Emma"}})
	if ran != "added Emma" {
		t.Errorf("a key for one app runs its tools: %q", ran)
	}
	ran = ""
	resp := rpcTo(app, "/app/pantry/", only, "tools/call", map[string]any{"name": "pantry_list"})
	if ran != "" || !strings.Contains(mustJSON(resp), "does not reach this app") {
		t.Errorf("a key for one app at another app's address: ran %q, %s", ran, mustJSON(resp))
	}
	if got := toolNames(rpcTo(app, "/app/pantry/", only, "tools/list", nil)); len(got) != 0 {
		t.Errorf("a key for one app lists another app's tools at its address: %v", got)
	}

	withAgent := mcpKey("user-a", customapps.AppToolsTarget+"pantry", "agent:helper")
	got := toolNames(rpcTo(app, "/", withAgent, "tools/list", nil))
	if !contains(got, "ask_agent") || !contains(got, "pantry_list") || contains(got, "books_add") {
		t.Errorf("a key naming an app and an agent: %v", got)
	}

	agentOnly := toolNames(rpcTo(app, "/", mcpKey("user-a", "agent:helper"), "tools/list", nil))
	if !contains(agentOnly, "books_add") || !contains(agentOnly, "pantry_list") || !contains(agentOnly, "ask_agent") {
		t.Errorf("a key naming an agent and no app reaches every app: %v", agentOnly)
	}

	all := toolNames(rpcTo(app, "/", mcpKey("user-a"), "tools/list", nil))
	if !contains(all, "books_add") || !contains(all, "pantry_list") || !contains(all, "ask_agent") {
		t.Errorf("a key naming no app reaches every app: %v", all)
	}
}

func TestAnAppsSlugIsReadFromItsAddress(t *testing.T) {
	for path, want := range map[string]string{
		"/app/reading/":     "reading",
		"/app/reading":      "reading",
		"/mcp/app/pantry/x": "pantry",
		"/app/":             "",
		"/":                 "",
	} {
		if got := appSlugFromPath(path); got != want {
			t.Errorf("%q: got %q, want %q", path, got, want)
		}
	}
}

// The app's address stays shut to a key that may not use it: one whose owner
// an administrator has not opened MCP to, and one narrowed to other tools.
func TestAnAppsAddressKeepsTheKeysLimits(t *testing.T) {
	ran := ""
	root := appsFixture(t, &ran)
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")

	narrowed := MintAccountTokenScoped("user-a", "narrow", &TokenScope{Features: []string{MCPFeatureKey}, Tools: &[]string{"ask_agent"}}).Token
	if got := toolNames(rpcTo(app, "/app/reading/", narrowed, "tools/list", nil)); len(got) != 0 {
		t.Errorf("a key narrowed to other tools lists the app's: %v", got)
	}
	resp := rpcTo(app, "/app/reading/", narrowed, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "x"}})
	if ran != "" || !strings.Contains(mustJSON(resp), "may not call") {
		t.Errorf("a key narrowed to other tools ran the app's: ran %q, %s", ran, mustJSON(resp))
	}

	key := mcpKey("user-a")
	SetFeatureAllowedUsers(RootDB, MCPFeatureKey, []string{"someone-else"})
	if got := toolNames(rpcTo(app, "/app/reading/", key, "tools/list", nil)); len(got) != 0 {
		t.Errorf("MCP closed to the owner, the app's address lists: %v", got)
	}
	resp = rpcTo(app, "/app/reading/", key, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "x"}})
	if ran != "" || !strings.Contains(mustJSON(resp), "has not enabled MCP") {
		t.Errorf("MCP closed to the owner, the app's tool ran: ran %q, %s", ran, mustJSON(resp))
	}
}

// The endpoint serves an app's address: a call to /app/<slug>/ reaches it.
func TestAnAppsAddressIsServed(t *testing.T) {
	ran := ""
	root := appsFixture(t, &ran)
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	mux := http.NewServeMux()
	app.SetWebMux(mux, "")
	app.Routes()
	r := httptest.NewRequest(http.MethodPost, "/app/reading/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.Header.Set("X-API-Key", mcpKey("user-a"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "books_add") || strings.Contains(w.Body.String(), "ask_agent") {
		t.Errorf("the app's address as served: %d %s", w.Code, w.Body.String())
	}
}

// An app shared with someone and offered to them is reached by their own
// key: one naming no app, or one pinned to the owner's app. A key naming the
// slug alone names an app of their own, so it does not reach the shared one.
func TestASharedAppIsReachedByAKeyPinnedToItsOwner(t *testing.T) {
	ran := ""
	root := appsFixture(t, &ran)
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")

	for _, key := range []string{mcpKey("user-c"), mcpKey("user-c", customapps.AppToolsTarget+"user-a/reading")} {
		ran = ""
		if got := toolNames(rpcTo(app, "/app/reading/", key, "tools/list", nil)); strings.Join(got, ",") != "books_add,reading_result" {
			t.Errorf("the shared app's tools: %v", got)
		}
		rpcTo(app, "/app/reading/", key, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "Dune"}})
		if ran != "user-c added Dune" {
			t.Errorf("the call ran as %q", ran)
		}
	}
	slugOnly := mcpKey("user-c", customapps.AppToolsTarget+"reading")
	ran = ""
	resp := rpcTo(app, "/app/reading/", slugOnly, "tools/call", map[string]any{"name": "books_add", "arguments": map[string]any{"title": "x"}})
	if ran != "" || !strings.Contains(mustJSON(resp), "does not reach this app") {
		t.Errorf("a key naming the slug alone reached the shared app: ran %q, %s", ran, mustJSON(resp))
	}
	if got := toolNames(rpcTo(app, "/", slugOnly, "tools/list", nil)); len(got) != 0 {
		t.Errorf("a key naming the slug alone lists the shared app at the general address: %v", got)
	}
}
