package core

// Per-agent workspaces: where each agent works, and what a delegated run
// inherits.

import (
	"context"
	"path/filepath"
	"testing"
)

func workspaceRoot(t *testing.T) string {
	t.Helper()
	saved := WorkspacesDir()
	dir := t.TempDir()
	SetWorkspacesDir(dir)
	t.Cleanup(func() { SetWorkspacesDir(saved) })
	return dir
}

// Agent dirs are SIBLINGS of the user root, never children, so no user root
// contains another family's files.
func TestAgentWorkspaceIsNotInsideTheUserRoot(t *testing.T) {
	workspaceRoot(t)
	root, err := EnsureWorkspaceDir("alice")
	if err != nil {
		t.Fatal(err)
	}
	agentDir, err := EnsureAgentWorkspaceDir("alice", "wren")
	if err != nil {
		t.Fatal(err)
	}
	if rel, _ := filepath.Rel(root, agentDir); rel != "" && rel[0] != '.' {
		t.Fatalf("agent workspace %q lives under the user root %q — a fallback read could reach it", agentDir, root)
	}
	other, err := EnsureAgentWorkspaceDir("alice", "other")
	if err != nil {
		t.Fatal(err)
	}
	if other == agentDir {
		t.Fatal("two agents share one directory")
	}
}

// A user root named .agents would contain every agent tree.
func TestAgentsDirNameIsReserved(t *testing.T) {
	workspaceRoot(t)
	if _, err := EnsureWorkspaceDir(AgentWorkspacesDirName); err == nil {
		t.Fatal("a user root named .agents was allowed; it would swallow every agent workspace")
	}
}

// Delegation carries the folder the delegator is working in AND whose it is,
// so the child can tell whether it is of the same family before adopting it.
func TestDelegationCarriesTheWorkspaceAndItsAgent(t *testing.T) {
	sess := &ToolSession{WorkspaceDir: "/tmp/parent-ws", AgentID: "wren"}
	ctx := sess.ContextWithNetworkConnector(context.Background())
	if dir, from := InheritedWorkspace(ctx); dir != "/tmp/parent-ws" || from != "wren" {
		t.Fatalf("inherited %q from %q, want the parent's workspace and the parent", dir, from)
	}
	// A run that starts outside any session inherits nothing and mints its own.
	if dir, from := InheritedWorkspace(context.Background()); dir != "" || from != "" {
		t.Errorf("bare context carried %q from %q", dir, from)
	}
}

// A detached run works where the turn that spawned it works.
func TestDetachedSessionKeepsTheWorkspace(t *testing.T) {
	parent := &ToolSession{Username: "alice", AgentID: "wren", WorkspaceDir: "/w/.agents/alice/wren"}
	if child := parent.ForDetachedTask(context.Background()); child.WorkspaceDir != parent.WorkspaceDir {
		t.Errorf("workspace = %q, want the parent's", child.WorkspaceDir)
	}
}
