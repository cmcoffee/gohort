package core

// An administrator's MCP server offered as one tool: an agent's catalog has
// the server's one tool or its separate tools, never both, as the server is
// set right now; and a grant naming them in the other shape reaches the
// shape that is, so switching keeps every agent that lists them.

import (
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/internal/mcpclient"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAServerOfferedAsOneToolOrMany(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	m := &MCPManager{db: db, conns: map[string]*mcpConn{}, userConns: map[string]*mcpConn{},
		proxies: map[string]*mcpProxyTool{}, whole: map[string]*mcpServerTool{}, oneTool: map[string]bool{},
		dialFail: map[string]time.Time{}, oauthPending: map[string]mcpPendingAuth{}}
	cfg := MCPServerConfig{Name: "onetooltest", URL: "https://mcp.invalid/mcp", ExposeTools: true, Enabled: true, OneTool: true}
	m.registerTools(cfg, []mcpclient.ToolDef{
		{Name: "search", Description: "Find.", InputSchema: map[string]any{"type": "object"}},
		{Name: "create_ticket", Description: "File one.", InputSchema: map[string]any{"type": "object"}},
	})
	// Set to one tool once (that registers it), then back, to start from
	// separate tools.
	sess := &ToolSession{Username: "u"}
	names := func(requested ...string) map[string]bool {
		t.Helper()
		defs, err := GetAgentToolsWithSession(sess, requested...)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, d := range defs {
			out[d.Tool.Name] = true
		}
		return out
	}

	// Many: the separate tools; the one tool offers nothing, and a grant of
	// it reaches the separate tools.
	if got := names("onetooltest_search", "onetooltest_create_ticket", "onetooltest"); len(got) != 2 || !got["onetooltest_search"] || got["onetooltest"] {
		t.Errorf("offered one by one: %v", got)
	}
	if got := names("onetooltest"); !got["onetooltest_search"] || !got["onetooltest_create_ticket"] {
		t.Errorf("a grant of the one tool should reach the separate ones: %v", got)
	}

	// Anything listing registered tools without following redirects still
	// sees one shape: the one tool offers nothing until the server is set so.
	proxy, _ := FindChatTool("onetooltest_search")
	whole, _ := FindChatTool("onetooltest")
	if !ChatToolAvailable(proxy, sess) || ChatToolAvailable(whole, sess) {
		t.Error("offered one by one, the separate tools are available and the one tool is not")
	}

	// One: the server's one tool, and a grant of a separate tool reaches it.
	m.mu.Lock()
	m.oneTool["onetooltest"] = true
	m.mu.Unlock()
	if got := names("onetooltest_search", "onetooltest_create_ticket", "onetooltest"); len(got) != 1 || !got["onetooltest"] {
		t.Errorf("offered as one: %v", got)
	}
	if got := names("onetooltest_search"); !got["onetooltest"] {
		t.Errorf("a grant of one of its tools should reach the one tool: %v", got)
	}
	if ChatToolAvailable(proxy, sess) || !ChatToolAvailable(whole, sess) {
		t.Error("offered as one, the one tool is available and the separate ones are not")
	}
	g := whole.(*mcpServerTool).grouped()
	if acts := g.ActionNames(); len(acts) != 2 {
		t.Errorf("an action per tool: %v", acts)
	}
	// A grant of one separate tool reaches one action of the one tool, so the
	// caller can tell it from a grant of all of them.
	if w, act := proxy.(ActionRedirectTool).RedirectAction(); w != "onetooltest" || act != "search" {
		t.Errorf("onetooltest_search should be the one tool's search action, got %q/%q", w, act)
	}
	if acts := ChatToolActionCaps(whole); len(acts) != 2 {
		t.Errorf("the one tool should name its actions, got %v", acts)
	}
}

// A server set to one tool whose one tool could not be registered (its name
// taken) keeps offering its tools one by one, rather than hiding them with
// nothing in their place.
func TestAOneToolWithNoNameLeavesTheToolsOffered(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	m := &MCPManager{db: db, conns: map[string]*mcpConn{}, userConns: map[string]*mcpConn{},
		proxies: map[string]*mcpProxyTool{}, whole: map[string]*mcpServerTool{}, oneTool: map[string]bool{},
		dialFail: map[string]time.Time{}, oauthPending: map[string]mcpPendingAuth{}}
	cfg := MCPServerConfig{Name: "nonametest", URL: "https://mcp.invalid/mcp", ExposeTools: true, Enabled: true}
	m.registerTools(cfg, []mcpclient.ToolDef{{Name: "search", Description: "Find.", InputSchema: map[string]any{"type": "object"}}})
	m.mu.Lock()
	m.oneTool["nonametest"] = true // set so, but never registered
	m.mu.Unlock()
	sess := &ToolSession{Username: "u"}
	proxy, _ := FindChatTool("nonametest_search")
	if !ChatToolAvailable(proxy, sess) {
		t.Error("its tool hid itself with no one tool to stand in for it")
	}
	defs, err := GetAgentToolsWithSession(sess, "nonametest_search")
	if err != nil || len(defs) != 1 || defs[0].Tool.Name != "nonametest_search" {
		t.Errorf("a grant of it: %v %v", defs, err)
	}
}
