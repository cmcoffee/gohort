package temptool

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// tool_def naming an app makes, shows, lists, changes and deletes a tool of
// that app's own space, never the person's tool of the same name; a session
// without the app's space wired (any agent but Builder) is refused.
func TestToolDefWorksOnAnAppsOwnTools(t *testing.T) {
	space := map[string]TempTool{}
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
		CanScopeGlobal: true,
	}
	if err := AdminPersistTempTool(sess.DB, "alice", TempTool{Name: "get_weather", Description: "the pool's", CommandTemplate: "echo pool"}); err != nil {
		t.Fatal(err)
	}
	td := BuildToolDef()
	create := map[string]any{"action": "create", "app": "wx", "name": "get_weather", "mode": "shell",
		"command_template": "echo app", "description": "Weather for the app."}
	if _, err := td.RunWithSession(create, sess); err == nil || !strings.Contains(err.Error(), "only Builder") {
		t.Fatalf("a session without the app's space: %v", err)
	}

	sess.AppTools = func(slug string) ([]TempTool, error) {
		if slug != "wx" {
			return nil, fmt.Errorf("no app %s", slug)
		}
		var out []TempTool
		for _, tt := range space {
			out = append(out, tt)
		}
		return out, nil
	}
	sess.SaveAppTool = func(slug string, tt TempTool) error { space[tt.Name] = tt; return nil }
	sess.DeleteAppTool = func(slug, name string) error { delete(space, name); return nil }

	out, err := td.RunWithSession(create, sess)
	if err != nil || !strings.Contains(out, "Saved to the app's own tools (wx)") {
		t.Fatalf("create: %v %s", err, out)
	}
	if space["get_weather"].CommandTemplate != "echo app" {
		t.Fatalf("the app's space after create: %+v", space)
	}
	if p, _ := UserToolByName(sess.DB, "alice", "get_weather"); p.Tool.Description != "the pool's" {
		t.Errorf("the person's tool of the same name changed: %+v", p.Tool)
	}
	if sess.AuthoringApp != "" {
		t.Error("the app stayed pointed at after the call")
	}
	if out, err := td.RunWithSession(map[string]any{"action": "update", "app": "wx", "name": "get_weather", "description": "Weather, kindly."}, sess); err != nil || space["get_weather"].Description != "Weather, kindly." {
		t.Fatalf("update: %v %s %+v", err, out, space["get_weather"])
	}
	if out, _ := td.RunWithSession(map[string]any{"action": "get", "app": "wx", "name": "get_weather"}, sess); !strings.Contains(out, "own tools") || !strings.Contains(out, "Weather, kindly.") {
		t.Errorf("get: %s", out)
	}
	if out, _ := td.RunWithSession(map[string]any{"action": "list", "app": "wx"}, sess); !strings.Contains(out, "get_weather") {
		t.Errorf("list: %s", out)
	}
	if _, err := td.RunWithSession(map[string]any{"action": "list", "app": "nope"}, sess); err == nil {
		t.Error("an app that is not the person's was listed")
	}
	if out, err := td.RunWithSession(map[string]any{"action": "delete", "app": "wx", "name": "get_weather"}, sess); err != nil || len(space) != 0 {
		t.Fatalf("delete: %v %s %+v", err, out, space)
	}
	if _, ok := UserToolByName(sess.DB, "alice", "get_weather"); !ok {
		t.Error("deleting the app's tool deleted the person's")
	}
}
