package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every tool_def path that writes or runs a tool asks the session's
// ToolEditRefusal first, for the tool and for each credential it names.
func TestToolDefAsksTheEditRefusal(t *testing.T) {
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
		ToolEditRefusal: func(tool, cred string) string {
			if tool == "kept_away" {
				return "NO TOOL"
			}
			if cred == "narrowed" {
				return "NO API"
			}
			return ""
		},
	}
	for name, call := range map[string]func() (string, error){
		"create": func() (string, error) {
			return createGrouped(map[string]any{"name": "kept_away", "description": "d", "command_template": "echo"}, sess)
		},
		"update": func() (string, error) {
			return updateGrouped(map[string]any{"name": "kept_away", "description": "d2"}, sess)
		},
		"delete": func() (string, error) { return deleteGrouped(map[string]any{"name": "kept_away"}, sess) },
		"test":   func() (string, error) { return testGrouped(map[string]any{"name": "kept_away"}, sess) },
	} {
		if out, _ := call(); out != "NO TOOL" {
			t.Errorf("%s went ahead: %q", name, out)
		}
	}
	for name, args := range map[string]map[string]any{
		"credential": {"name": "fresh", "mode": "api", "description": "d", "url_template": "https://x.test/", "credential": "narrowed"},
		"fetch_via":  {"name": "fresh", "description": "d", "command_template": "echo", "hook_capabilities": []any{"fetch_via:narrowed"}},
	} {
		if out, _ := createGrouped(args, sess); out != "NO API" {
			t.Errorf("%s: a tool naming a refused API was created: %q", name, out)
		}
	}
}
