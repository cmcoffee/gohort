package orchestrate

// Each agent family works in a folder of its own and reads nothing outside it:
// a top-level agent's folder, which its sub-agents share. Delegation hands the
// delegator's folder on within a family and nowhere else.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A background task writes into the agent's directory. The wake that reports
// it rides the scheduled-fire path and gets a hand-built session. Before the
// fix that session ran at the user root, so the bare filename the wake carried
// resolved to nothing and the model re-downloaded the whole file.
func TestWakeFindsWhatTheAgentJustWrote(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner, agentID, name = "owner@example.test", "b625cc9f", "video-dl1sliq5gdw0.mp4"

	agentDir, err := EnsureAgentWorkspaceDir(owner, agentID)
	if err != nil {
		t.Fatalf("agent workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, name), []byte("mp4"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	dir := agentWorkspace(nil, owner, AgentRecord{ID: agentID})
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("wake could not reach the agent's own artifact in %s: %v", dir, err)
	}
}

// No agent id (a CLI turn, a bare tool call) keeps the user's root.
func TestNoAgentKeepsTheRoot(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	root, err := EnsureWorkspaceDir("owner@example.test")
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if dir := agentWorkspace(nil, "owner@example.test", AgentRecord{}); dir != root {
		t.Fatalf("no-agent turn: dir=%q, want %q", dir, root)
	}
}

func TestAgentFamiliesWorkInFoldersOfTheirOwn(t *testing.T) {
	_, udb := familyFixture(t)
	SetWorkspacesDir(t.TempDir())
	if _, err := saveAgent(udb, AgentRecord{ID: "a-other", Name: "Other", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	load := func(id string) AgentRecord {
		a, ok := loadAgent(udb, id)
		if !ok {
			t.Fatalf("no %s", id)
		}
		return a
	}
	writer, critic, nit, other := load("a-writer"), load("a-critic"), load("a-nit"), load("a-other")
	turn := func(a AgentRecord, ctx context.Context) *chatTurn {
		return &chatTurn{user: "alice", udb: udb, agent: a, ctx: ctx}
	}
	home, _ := turn(writer, nil).turnWorkspace()
	root, _ := EnsureWorkspaceDir("alice")
	if home == "" || home == root {
		t.Fatalf("the writer works in %q, want a folder of its own", home)
	}

	// Sub-agents, at any depth, work in their top-level parent's folder.
	for _, a := range []AgentRecord{critic, nit} {
		if got, _ := turn(a, nil).turnWorkspace(); got != home {
			t.Errorf("%s works in %q, want its top-level parent's %q", a.ID, got, home)
		}
	}
	// Another top-level agent does not.
	away, _ := turn(other, nil).turnWorkspace()
	if away == home || away == root {
		t.Fatalf("another agent works in %q", away)
	}

	// A wake or a machine run knows its agent by id: the same folders.
	if got := familyWorkspace(udb, "alice", "a-nit"); got != home {
		t.Errorf("a sub-agent's wake works in %q, want %q", got, home)
	}
	if got := familyWorkspace(udb, "alice", ""); got != root {
		t.Errorf("a run with no agent works in %q, want the user's root %q", got, root)
	}

	// Delegating inline: a sub-agent works where the delegator is working,
	// even a project workspace; another family in its own folder.
	project := filepath.Join(t.TempDir(), "project")
	working := turn(writer, WithWorkspaceDir(context.Background(), project, "a-writer"))
	if got, _ := working.delegateWorkspace(critic); got != project {
		t.Errorf("a sub-agent sent work runs in %q, want where its parent is working %q", got, project)
	}
	if got, _ := working.delegateWorkspace(other); got != away {
		t.Errorf("another family sent work runs in %q, want its own %q", got, away)
	}
	// A folder handed down by another family is not adopted.
	foreign := turn(writer, WithWorkspaceDir(context.Background(), project, "a-other"))
	if got, _ := foreign.delegateWorkspace(critic); got != home {
		t.Errorf("a sub-agent adopted another family's folder: %q", got)
	}

	// The same rule on the dispatch paths.
	ctx := WithWorkspaceDir(context.Background(), project, "a-critic")
	if got := delegatedWorkspace(ctx, udb, "alice", nit); got != project {
		t.Errorf("dispatched within the family: %q, want %q", got, project)
	}
	if got := delegatedWorkspace(ctx, udb, "alice", other); got != away {
		t.Errorf("dispatched to another family: %q, want %q", got, away)
	}
}

// A large upload is written where the agent's turn will read it: its own
// folder. It went to the user root once, where every agent could read it.
func TestALargeUploadLandsInTheAgentsFolder(t *testing.T) {
	_, udb := familyFixture(t)
	SetWorkspacesDir(t.TempDir())
	writer, _ := loadAgent(udb, "a-writer")
	sr := &sendRun{udb: udb, user: "alice", agent: writer, sess: ChatSession{ID: "s1"},
		req: sendRequest{Message: "read this"},
		extractedAttachments: []extractedAttachment{{name: "big.txt", mime: "text/plain",
			text: strings.Repeat("a line of the upload\n", attachmentSpillThresholdBytes()/10+10)}}}
	sr.attachmentPreamble()
	home := agentWorkspace(udb, "alice", writer)
	if _, err := os.Stat(filepath.Join(home, attachmentSpillDirName)); err != nil {
		t.Errorf("the upload is not in the agent's folder: %v\n%s", err, sr.req.Message)
	}
	root, _ := EnsureWorkspaceDir("alice")
	if _, err := os.Stat(filepath.Join(root, attachmentSpillDirName)); err == nil {
		t.Error("the upload was written to the user's root")
	}
}

// A turn that never switched workspaces reports no managed id, so the delegate
// does not inherit one that was never chosen. (The managed-workspace branch
// itself needs a live store and is covered where that store is available.)
func TestATurnWithNoManagedWorkspaceReportsNone(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	const owner = "owner@example.test"

	plain := &chatTurn{user: owner, agent: AgentRecord{ID: "parent-agent"}}
	agentDir, wsID := plain.turnWorkspace()
	if wsID != "" {
		t.Fatalf("a turn with no managed workspace reported one: %q", wsID)
	}
	if agentDir == "" {
		t.Fatal("no directory at all")
	}
}
