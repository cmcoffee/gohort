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

// TestBuilderPoolsNotBundlesWithAgentID reproduces the real-world strand the
// scope-message test above missed: a live Builder session has AgentID SET, and
// the old persistUnapprovedTool agent-bundled to it and returned before saving
// the session draft finalizeAuthoredTool needs — so the tool was stuck
// agent-bundled (Trial) instead of reaching the user-wide pool, and edits never
// propagated to the user's other agents. Builder must POOL, never bundle.
func TestBuilderPoolsNotBundlesWithAgentID(t *testing.T) {
	prev := AttachToolToAgent
	defer func() { AttachToolToAgent = prev }()
	bundled := false
	AttachToolToAgent = func(db Database, owner, agentID string, tt TempTool) error {
		bundled = true // Builder must NOT reach this
		return nil
	}

	db := &DBase{Store: kvlite.MemStore()}
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(),
		DB:             db,
		AgentID:        "seed-builder", // the field that triggered the strand
		CanScopeGlobal: true,
	}
	out, err := createGrouped(map[string]any{
		"action": "create", "name": "vapi_calls", "description": "d", "mode": "shell",
		"command_template": "python3 {workspace_dir}/z.py", "script_body": "print('hi')", "script_name": "z.py",
	}, sess)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if bundled {
		t.Error("Builder must not agent-bundle — its tools go to the user-wide pool")
	}
	if !strings.Contains(out, "Saved to your tools") {
		t.Errorf("expected pool-save message; got:\n%s", out)
	}
	found := false
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.Tool.Name == "vapi_calls" {
			found = true
		}
	}
	if !found {
		t.Error("tool should be in the user-wide pool, reachable by every one of alice's agents")
	}
}
