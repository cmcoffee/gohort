package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A background task writes into the agent's directory. The wake that reports
// it rides the scheduled-fire path and gets a hand-built session. Before the
// fix that session ran at the user root, so the bare filename the wake carried
// resolved to nothing and the model re-downloaded the whole file.
func TestWakeFindsWhatTheAgentJustWrote(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner, agentID, name = "cmcoffee@gmail.com", "b625cc9f", "video-dl1sliq5gdw0.mp4"

	agentDir, err := EnsureAgentWorkspaceDir(owner, agentID)
	if err != nil {
		t.Fatalf("agent workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, name), []byte("mp4"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	dir, fallback := agentTurnWorkspace(owner, agentID)
	sess := &ToolSession{Username: owner, WorkspaceDir: dir, WorkspaceFallback: fallback}
	abs, err := ResolveWorkspaceRead(sess, name)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("wake could not reach the agent's own artifact at %s: %v", abs, err)
	}
}

// The root stays readable from inside an agent's directory, so a file a person
// dropped in their own workspace is still reachable from a fire.
func TestFireStillReadsTheUserRoot(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner, agentID = "cmcoffee@gmail.com", "b625cc9f"

	root, err := EnsureWorkspaceDir(owner)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	dir, fallback := agentTurnWorkspace(owner, agentID)
	if dir == root {
		t.Fatalf("fire ran at the user root, not the agent's own directory")
	}
	if fallback != root {
		t.Fatalf("read fallback = %q, want the user root %q", fallback, root)
	}
	sess := &ToolSession{Username: owner, WorkspaceDir: dir, WorkspaceFallback: fallback}
	abs, err := ResolveWorkspaceRead(sess, "notes.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("user root unreachable from the agent dir: %v", err)
	}
}

// No agent id (a CLI turn, a bare tool call) keeps today's behavior: the root,
// and no fallback to itself.
func TestNoAgentKeepsTheRoot(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	root, err := EnsureWorkspaceDir("cmcoffee@gmail.com")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	dir, fallback := agentTurnWorkspace("cmcoffee@gmail.com", "")
	if dir != root || fallback != "" {
		t.Fatalf("no-agent turn: dir=%q fallback=%q, want %q and empty", dir, fallback, root)
	}
}
