// Per-agent workspaces: each agent family works in a folder of its own.
//
// The shared per-user root has no attribution: every agent writes into one
// directory, so an agent can list a file another agent produced, attach it, and
// report it as its own work, and a file one agent holds reaches every other. So
// a turn runs in its agent FAMILY's directory: a top-level agent's own, which
// its sub-agents share (they work for it, and what they make it delivers).
//
// Nothing reads outside it. An earlier version let reads fall back to the user
// root, so files a person put there stayed reachable from every agent; that is
// the same reach from one agent into another's files, by way of the root, and
// it went. Durable work still follows the user, which is why per-SESSION
// isolation failed: a family's folder outlives every conversation it has.
//
// DELEGATION within a family hands the delegator's current workspace to the
// child, so a sub-agent producing something its parent will deliver writes
// where the parent is working, a project workspace included. Between families
// nothing is shared: what one hands another goes as an attachment.
//
// Agent directories are SIBLINGS of the user roots, never children, so no
// user root contains another family's files.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentWorkspacesDirName is the reserved directory under the workspaces root
// that holds per-agent trees: <workspaces>/.agents/<user>/<agent>. Reserved
// means EnsureWorkspaceDir refuses it as a username, so a user cannot be given
// a root that swallows every agent's workspace.
const AgentWorkspacesDirName = ".agents"

// EnsureAgentWorkspaceDir returns the workspace for one agent of one user,
// creating it if needed. Falls back to the user root when there is no agent id
// — a turn with no agent (CLI, a bare tool call) keeps today's behavior.
func EnsureAgentWorkspaceDir(userID, agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return EnsureWorkspaceDir(userID)
	}
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured (SetWorkspacesDir not called)")
	}
	if err := validWorkspaceSegment(userID, "userID"); err != nil {
		return "", err
	}
	if err := validWorkspaceSegment(agentID, "agentID"); err != nil {
		return "", err
	}
	dir := filepath.Join(base, AgentWorkspacesDirName, userID, agentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create agent workspace: %w", err)
	}
	return dir, nil
}

// validWorkspaceSegment rejects anything that could escape or reserve a path
// element. Same rules EnsureWorkspaceDir applies to a username, applied to
// both segments here.
func validWorkspaceSegment(seg, label string) error {
	if seg == "" {
		return fmt.Errorf("%s required for workspace resolution", label)
	}
	if strings.ContainsAny(seg, `/\`) || strings.Contains(seg, "..") || seg == "." {
		return fmt.Errorf("invalid %s: %q", label, seg)
	}
	return nil
}

// --- delegation: the child runs where its parent is running -----------------

type workspaceCtxKey struct{}

// inheritedWorkspace is what a delegated run may adopt: the directory the
// delegator is working in, and whose it is.
type inheritedWorkspace struct {
	dir, agentID string
}

// WithWorkspaceDir stamps the workspace a delegated run may inherit, and the
// agent working in it. Carried on the context rather than threaded through
// every dispatch signature, the same way the turn's network connector travels
// — and for the same reason: every path that can start a sub-run already
// passes a context, and none of them should have to know about this.
func WithWorkspaceDir(ctx context.Context, dir, agentID string) context.Context {
	if strings.TrimSpace(dir) == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceCtxKey{}, inheritedWorkspace{dir: dir, agentID: strings.TrimSpace(agentID)})
}

// InheritedWorkspace returns the workspace a delegated run was handed and the
// agent that was working in it, or "" when it is starting fresh. The child
// adopts it only when it is of the same family (see the package comment): a
// sub-agent producing something its parent will deliver writes where the
// parent is working.
func InheritedWorkspace(ctx context.Context) (dir, agentID string) {
	if ctx == nil {
		return "", ""
	}
	w, _ := ctx.Value(workspaceCtxKey{}).(inheritedWorkspace)
	return w.dir, w.agentID
}
