package temptool

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A script may call the owner's own active tool, and not one that would stop
// to ask (a page load has nobody to answer), nor one the owner does not have.
func TestAScriptCallsOnlyToolsThatNeedNoOne(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	if err := AdminPersistTempTool(RootDB, "owner", TempTool{Name: "get_weather", Description: "d", CommandTemplate: "echo sunny", HookCapabilities: []string{"fetch"}}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "owner", TempTool{Name: "send_page", Description: "d", CommandTemplate: "echo x", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "someone", TempTool{Name: "their_tool", Description: "d", CommandTemplate: "echo y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ScriptCallableTool(RootDB, "owner", "get_weather"); err != nil {
		t.Fatalf("the owner's quiet tool: %v", err)
	}
	if _, err := ScriptCallableTool(RootDB, "owner", "send_page"); err == nil || !strings.Contains(err.Error(), "nobody to ask") {
		t.Fatalf("an ask-first tool: %v", err)
	}
	if _, err := ScriptCallableTool(RootDB, "owner", "their_tool"); err == nil || !strings.Contains(err.Error(), "no tool named") {
		t.Fatalf("another user's tool: %v", err)
	}
	if _, err := CallToolForScript(&ToolSession{}, "", "get_weather", nil); err == nil {
		t.Fatal("a session with no user ran a tool")
	}
}

// fakeMCPTool stands in for a registered MCP proxy: it carries its server's
// category and runs per user, recording whose session called it.
type fakeMCPTool struct {
	name, server string
	confirm      bool
	ranAs        *string
}

func (f fakeMCPTool) Name() string                 { return f.name }
func (f fakeMCPTool) Desc() string                 { return "MCP tool." }
func (f fakeMCPTool) Params() map[string]ToolParam { return nil }
func (f fakeMCPTool) Run(map[string]any) (string, error) {
	return "", fmt.Errorf("run without a session")
}
func (f fakeMCPTool) Category() string   { return MCPToolCategory(f.server) }
func (f fakeMCPTool) NeedsConfirm() bool { return f.confirm }
func (f fakeMCPTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	*f.ranAs = sess.Username
	return "issue " + fmt.Sprint(args["key"]), nil
}

// A script may call an MCP tool from an enabled server that offers its tools,
// as the person using the app (so a per-person sign-in answers for them), and
// not one that would stop to ask, nor one from a server that is off.
func TestAScriptCallsAnMCPToolAsTheAppsUser(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	db := &DBase{Store: kvlite.MemStore()}
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	if err := MCP().Save(MCPServerConfig{Name: "scripttest-atl", URL: "http://127.0.0.1:1/mcp", Enabled: true, ExposeTools: true}, ""); err != nil {
		t.Fatal(err)
	}
	MCP().Save(MCPServerConfig{Name: "scripttest-off", URL: "http://127.0.0.1:1/mcp", ExposeTools: true}, "")
	t.Cleanup(func() { MCP().Delete("scripttest-atl"); MCP().Delete("scripttest-off") })
	var ranAs string
	RegisterChatTool(fakeMCPTool{name: "scripttest-atl_get_issue", server: "scripttest-atl", ranAs: &ranAs})
	RegisterChatTool(fakeMCPTool{name: "scripttest-atl_delete_issue", server: "scripttest-atl", confirm: true, ranAs: &ranAs})
	RegisterChatTool(fakeMCPTool{name: "scripttest-off_get_page", server: "scripttest-off", ranAs: &ranAs})

	if _, err := ScriptCallableTool(db, "owner", "scripttest-atl_get_issue"); err != nil {
		t.Fatalf("an MCP read tool: %v", err)
	}
	out, err := CallToolForScript(&ToolSession{Username: "owner", DB: db}, "bob", "scripttest-atl_get_issue", map[string]any{"key": "SUP-1"})
	if err != nil || out != "issue SUP-1" || ranAs != "bob" {
		t.Errorf("ran as %q: %q %v", ranAs, out, err)
	}
	if _, err := ScriptCallableTool(db, "owner", "scripttest-atl_delete_issue"); err == nil || !strings.Contains(err.Error(), "nobody to ask") {
		t.Errorf("an MCP tool that asks first: %v", err)
	}
	if _, err := ScriptCallableTool(db, "owner", "scripttest-off_get_page"); err == nil {
		t.Error("a tool from a server that is off was callable")
	}
}
