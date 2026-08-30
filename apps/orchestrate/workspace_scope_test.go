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

// A delegated sub-agent runs where its delegator runs. agents(run) returns
// TEXT — no attachments — so a file the sub-agent made reaches its parent by
// path or not at all, and the two have to share a directory.
//
// The shared user root was the directory they shared, which is why it worked
// and why it was wrong: every delegated agent wrote into one place. The
// delegator's OWN directory shares it with the one agent that needs it.
func TestDelegateRunsWhereItsDelegatorRuns(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner, parentID = "cmcoffee@gmail.com", "parent-agent"

	parent := &chatTurn{user: owner, agent: AgentRecord{ID: parentID}}
	dir, _, fallback := parent.turnWorkspace()

	root, err := EnsureWorkspaceDir(owner)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if dir == root {
		t.Fatal("the delegator itself is running at the shared root")
	}
	if fallback != root {
		t.Fatalf("read fallback = %q, want the user root", fallback)
	}

	// What the sub-agent writes, the parent finds by name.
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("findings"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	parentSess := &ToolSession{Username: owner, WorkspaceDir: dir, WorkspaceFallback: fallback}
	abs, err := ResolveWorkspaceRead(parentSess, "report.md")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("parent could not read what its delegate wrote: %v", err)
	}

	// And another agent's directory is not it.
	other := &chatTurn{user: owner, agent: AgentRecord{ID: "other-agent"}}
	if od, _, _ := other.turnWorkspace(); od == dir {
		t.Fatal("two agents resolved to the same directory")
	}
}

// A turn that never switched workspaces reports no managed id, so the delegate
// does not inherit one that was never chosen. (The managed-workspace branch
// itself needs a live store and is covered where that store is available.)
func TestATurnWithNoManagedWorkspaceReportsNone(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner = "cmcoffee@gmail.com"

	plain := &chatTurn{user: owner, agent: AgentRecord{ID: "parent-agent"}}
	agentDir, wsID, _ := plain.turnWorkspace()
	if wsID != "" {
		t.Fatalf("a turn with no managed workspace reported one: %q", wsID)
	}
	if agentDir == "" {
		t.Fatal("no directory at all")
	}
}
