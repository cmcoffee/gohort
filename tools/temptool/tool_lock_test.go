package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestLockedToolGuard: a LOCKED tool in the user's pool can't be modified,
// deleted, or overwritten by the AI's tool_def — the guard steers it to unlock
// in the UI instead. (User-only control, like Secured on credentials.)
func TestLockedToolGuard(t *testing.T) {
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
	}
	if err := AdminPersistTempTool(sess.DB, "alice", TempTool{
		Name: "cal", Mode: TempToolModeShell, CommandTemplate: "echo hi", Locked: true,
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	if out, _ := updateGrouped(map[string]any{"name": "cal", "description": "x"}, sess); !strings.Contains(out, "LOCKED") {
		t.Fatalf("update of a locked tool should be refused; got: %s", out)
	}
	if out, _ := deleteGrouped(map[string]any{"name": "cal"}, sess); !strings.Contains(out, "LOCKED") {
		t.Fatalf("delete of a locked tool should be refused; got: %s", out)
	}
	if out, _ := createGrouped(map[string]any{
		"action": "create", "name": "cal", "mode": "shell",
		"command_template": "echo x", "description": "y",
	}, sess); !strings.Contains(out, "LOCKED") {
		t.Fatalf("create-over a locked tool should be refused; got: %s", out)
	}
}

// TestGovernanceFlagsPreservedOnRepersist: the user-set Locked/Disabled/
// BuilderOnly flags survive an AI re-persist (a Builder edit that reconstructs
// the record), so an edit can't silently re-enable a disabled tool.
func TestGovernanceFlagsPreservedOnRepersist(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if err := AdminPersistTempTool(db, "alice", TempTool{
		Name: "diag", Mode: TempToolModeShell, CommandTemplate: "echo hi",
		Disabled: true, BuilderOnly: true,
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// AI re-persists a fresh version with the flags cleared (as tool_def would).
	if err := AdminPersistTempTool(db, "alice", TempTool{
		Name: "diag", Mode: TempToolModeShell, CommandTemplate: "echo NEW",
	}); err != nil {
		t.Fatalf("re-persist: %v", err)
	}
	var got *TempTool
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.Tool.Name == "diag" {
			tp := p.Tool
			got = &tp
		}
	}
	if got == nil {
		t.Fatal("tool disappeared")
	}
	if !got.Disabled || !got.BuilderOnly {
		t.Fatalf("flags not preserved: disabled=%v builderOnly=%v", got.Disabled, got.BuilderOnly)
	}
	if got.CommandTemplate != "echo NEW" {
		t.Fatalf("the edit itself should apply; got %q", got.CommandTemplate)
	}
}
