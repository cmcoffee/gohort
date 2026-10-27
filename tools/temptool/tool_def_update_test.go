package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestToolChangesAskTheOwner: a change to a tool that already exists in the
// user's pool (update, delete, create over it) asks ToolSession.ApproveChange
// first, and a no is returned instead of the change; a brand-new tool is
// reported as new. (Replaced Lock: every existing tool is protected this way.)
func TestToolChangesAskTheOwner(t *testing.T) {
	type call struct {
		name, detail string
		op           ChangeOp
	}
	var calls []call
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
		ApproveChange: func(kind, name, detail string, op ChangeOp) string {
			calls = append(calls, call{name, detail, op})
			if op != ChangeNew {
				return "NOT DONE: declined"
			}
			return ""
		},
	}
	if err := AdminPersistTempTool(sess.DB, "alice", TempTool{
		Name: "cal", Mode: TempToolModeShell, CommandTemplate: "echo hi",
	}); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if out, _ := updateGrouped(map[string]any{"name": "cal", "description": "x"}, sess); !strings.Contains(out, "declined") {
		t.Fatalf("an update went ahead unapproved; got: %s", out)
	}
	if out, _ := deleteGrouped(map[string]any{"name": "cal"}, sess); !strings.Contains(out, "declined") {
		t.Fatalf("a delete went ahead unapproved; got: %s", out)
	}
	if out, _ := createGrouped(map[string]any{
		"action": "create", "name": "cal", "mode": "shell",
		"command_template": "echo x", "description": "y",
	}, sess); !strings.Contains(out, "declined") {
		t.Fatalf("a create over an existing tool went ahead unapproved; got: %s", out)
	}
	if len(calls) != 3 || !strings.Contains(calls[0].detail, "description") || !strings.HasPrefix(calls[1].detail, "Delete it.") ||
		calls[0].op != ChangeEdit || calls[1].op != ChangeDelete || calls[2].op != ChangeEdit {
		t.Errorf("approval asked with %+v", calls)
	}
	calls = nil
	createGrouped(map[string]any{"action": "create", "name": "brand_new", "mode": "shell", "command_template": "echo x", "description": "y"}, sess)
	if len(calls) != 1 || calls[0].op != ChangeNew {
		t.Errorf("a new tool was not reported as new: %+v", calls)
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

// A delete asks once, says what it costs and who loses the tool, and its
// result names them again: a Builder that looked for holders in the agent
// list (which shows no tools) deleted tools two agents had, and said none did.
func TestADeleteSaysWhoLosesTheTool(t *testing.T) {
	prev := ToolHolders
	ToolHolders = func(db Database, owner, name string) []string {
		if name == "cal" {
			return []string{"Agent One", "Agent Two"}
		}
		return nil
	}
	t.Cleanup(func() { ToolHolders = prev })
	var details []string
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
		ApproveChange: func(kind, name, detail string, op ChangeOp) string {
			details = append(details, detail)
			return ""
		},
	}
	for _, n := range []string{"cal", "spare"} {
		if err := AdminPersistTempTool(sess.DB, "alice", TempTool{Name: n, Mode: TempToolModeShell, CommandTemplate: "echo hi"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := deleteGrouped(map[string]any{"name": "cal"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || !strings.Contains(details[0], "rebuild rarely") || !strings.Contains(details[0], "Agent One, Agent Two use it") {
		t.Errorf("the delete should ask once, with its cost and who loses it: %q", details)
	}
	if !strings.Contains(out, "Agent One, Agent Two had it and no longer do") {
		t.Errorf("the result should say who lost it: %s", out)
	}
	out, _ = deleteGrouped(map[string]any{"name": "spare"}, sess)
	if strings.Contains(out, "no longer do") || strings.Contains(details[1], "would lose it") {
		t.Errorf("a tool nobody had names nobody: %q / %s", details[1], out)
	}
}
