package core

// Per-agent workspaces: what each agent may write, what it may still read, and
// what a delegated run inherits.

import (
	"context"
	"os"
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

// The leak this exists to close: agent dirs must be SIBLINGS of the user root,
// never children. Underneath it, a fallback read of "…/other/notes.txt" would
// pass the root's containment check and hand one agent another's files.
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

func TestReadFallsBackToTheRootButWritesDoNot(t *testing.T) {
	workspaceRoot(t)
	root, _ := EnsureWorkspaceDir("alice")
	agentDir, _ := EnsureAgentWorkspaceDir("alice", "wren")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("personal"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", AgentID: "wren", WorkspaceDir: agentDir, WorkspaceFallback: root}

	// Reachable, because a person put it at their own root.
	got, err := ResolveWorkspaceRead(sess, "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "notes.txt") {
		t.Errorf("read resolved to %q, want the root copy", got)
	}
	// A file the agent has its own copy of resolves to ITS copy, not the root's.
	if err := os.WriteFile(filepath.Join(agentDir, "notes.txt"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	got, _ = ResolveWorkspaceRead(sess, "notes.txt")
	if got != filepath.Join(agentDir, "notes.txt") {
		t.Errorf("own copy must win; got %q", got)
	}
	// A path that exists NOWHERE reports against the agent's own dir, so the
	// error names where the write would have gone.
	got, _ = ResolveWorkspaceRead(sess, "missing.txt")
	if got != filepath.Join(agentDir, "missing.txt") {
		t.Errorf("unresolved path = %q, want the agent's own dir", got)
	}
}

// Traversal must still report as traversal, not as a miss that quietly falls
// through to the other root.
func TestReadFallbackDoesNotWeakenContainment(t *testing.T) {
	workspaceRoot(t)
	root, _ := EnsureWorkspaceDir("alice")
	agentDir, _ := EnsureAgentWorkspaceDir("alice", "wren")
	sess := &ToolSession{WorkspaceDir: agentDir, WorkspaceFallback: root}
	for _, bad := range []string{"../escape.txt", "/etc/passwd"} {
		if _, err := ResolveWorkspaceRead(sess, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestDelegationCarriesTheWorkspace(t *testing.T) {
	sess := &ToolSession{WorkspaceDir: "/tmp/parent-ws"}
	ctx := sess.ContextWithNetworkConnector(context.Background())
	if got := InheritedWorkspaceDir(ctx); got != "/tmp/parent-ws" {
		t.Fatalf("inherited %q, want the parent's workspace — a sub-agent's output must land where its parent will read it", got)
	}
	// A run that starts outside any session inherits nothing and mints its own.
	if got := InheritedWorkspaceDir(context.Background()); got != "" {
		t.Errorf("bare context carried %q", got)
	}
}

// A detached run must resolve paths exactly as the turn that spawned it does.
// Anything the turn could read, the background half has to be able to read too
// — otherwise the failure only appears in the path nobody exercises by hand.
func TestDetachedSessionKeepsTheReadFallback(t *testing.T) {
	parent := &ToolSession{
		Username: "alice", AgentID: "wren",
		WorkspaceDir: "/w/.agents/alice/wren", WorkspaceFallback: "/w/alice",
	}
	child := parent.ForDetachedTask(context.Background())
	if child.WorkspaceDir != parent.WorkspaceDir {
		t.Errorf("workspace = %q, want the parent's", child.WorkspaceDir)
	}
	if child.WorkspaceFallback != parent.WorkspaceFallback {
		t.Errorf("fallback = %q, want %q — a detached run would not find files at the user root", child.WorkspaceFallback, parent.WorkspaceFallback)
	}
}
