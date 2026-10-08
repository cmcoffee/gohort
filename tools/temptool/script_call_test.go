package temptool

import (
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
	if _, err := CallToolForScript(&ToolSession{}, "get_weather", nil); err == nil {
		t.Fatal("a session with no user ran a tool")
	}
}
