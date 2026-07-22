package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCreateScopeMessage locks the fix for the stale "an admin must promote
// this" ceremony: with the my-tools-vs-admin-tools model, a Builder-authored
// tool auto-persists to the user's pool, so the create result must say so — not
// "available for this session; admin promotes". A non-global agent still gets
// the session-scoped line.
func TestCreateScopeMessage(t *testing.T) {
	newSess := func(global bool) *ToolSession {
		return &ToolSession{
			Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(),
			DB:             &DBase{Store: kvlite.MemStore()},
			CanScopeGlobal: global,
		}
	}

	// Builder (global scope): auto-saved to the user's pool, no admin.
	out, err := createGrouped(map[string]any{
		"action": "create", "name": "b_tool", "description": "d", "mode": "shell",
		"command_template": "python3 {workspace_dir}/x.py", "script_body": "print('hi')", "script_name": "x.py",
	}, newSess(true))
	if err != nil {
		t.Fatalf("builder create: %v", err)
	}
	if !strings.Contains(out, "Saved to your tools") {
		t.Fatalf("builder create should report auto-save to the user's pool; got:\n%s", out)
	}
	for _, stale := range []string{"admin promotes", "admin) can promote", "rest of this conversation", "admin needs", "pending-tools"} {
		if strings.Contains(out, stale) {
			t.Fatalf("builder create must NOT contain stale promotion ceremony %q; got:\n%s", stale, out)
		}
	}

	// Non-global agent with no bundle target: honest session-scoped message.
	out, err = createGrouped(map[string]any{
		"action": "create", "name": "a_tool", "description": "d", "mode": "shell",
		"command_template": "python3 {workspace_dir}/y.py", "script_body": "print('hi')", "script_name": "y.py",
	}, newSess(false))
	if err != nil {
		t.Fatalf("agent create: %v", err)
	}
	if !strings.Contains(out, "THIS session") {
		t.Fatalf("non-global create should report session scope; got:\n%s", out)
	}
}
