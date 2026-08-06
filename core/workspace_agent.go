// Per-agent workspaces, and the fallback that keeps the user's own files
// reachable from all of them.
//
// The shared per-user root has no attribution: every agent writes into one
// directory, so an agent can list a file another agent produced, attach it, and
// report it as its own work. That is the failure the image ring is already
// scoped per user AND agent to avoid — "a picture I can point at" has to mean
// one picture — and raw filenames were the hole left in it.
//
// So a turn runs in ITS AGENT's directory. Two rules keep that from becoming
// the isolation that was rejected twice before:
//
//   - READS fall back to the user root. Files a person put there, or that
//     predate this, stay reachable from every agent. Durable and personal work
//     still follows the user, which is why per-SESSION isolation failed.
//   - DELEGATION hands the parent's current workspace to the child, so the one
//     case where two agents genuinely need the same files — a sub-agent
//     producing something its parent will deliver — shares by construction.
//
// Agent directories are SIBLINGS of the user root, never children. If they sat
// underneath it, a fallback read of "agents/other/notes.txt" would resolve
// inside the root's containment check and hand one agent another's files — the
// exact leak this is here to close.
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

// ResolveWorkspaceRead resolves a path for READING. It looks in the session's
// own workspace first, then — only if nothing is there — in the fallback root.
// Writes deliberately do NOT get this treatment: an agent writes into its own
// directory, always, or the isolation is decorative.
//
// Returns the primary resolution's error when the path is invalid, so a
// traversal attempt reports as one rather than as "not found".
func ResolveWorkspaceRead(sess *ToolSession, rel string) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("session required")
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return abs, nil
	}
	fallback := strings.TrimSpace(sess.WorkspaceFallback)
	if fallback == "" || fallback == sess.WorkspaceDir {
		return abs, nil // no fallback configured; report the primary path
	}
	alt, altErr := ResolveWorkspacePath(fallback, rel)
	if altErr != nil {
		return abs, nil // fallback rejected it; the primary path owns the error
	}
	if _, statErr := os.Stat(alt); statErr == nil {
		return alt, nil
	}
	return abs, nil // in neither; the caller's "not found" should name its own
}

// --- delegation: the child runs where its parent is running -----------------

type workspaceCtxKey struct{}

// WithWorkspaceDir stamps the workspace a delegated run should inherit. Carried
// on the context rather than threaded through every dispatch signature, the
// same way the turn's network connector travels — and for the same reason:
// every path that can start a sub-run already passes a context, and none of
// them should have to know about this.
func WithWorkspaceDir(ctx context.Context, dir string) context.Context {
	if strings.TrimSpace(dir) == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceCtxKey{}, dir)
}

// InheritedWorkspaceDir returns the workspace a delegated run should adopt, or
// "" when it is starting fresh. The child runs in its PARENT's directory rather
// than its own: a sub-agent producing something its parent will deliver is the
// one case where two agents genuinely need the same files, and it is exactly
// the case hard isolation breaks.
func InheritedWorkspaceDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	dir, _ := ctx.Value(workspaceCtxKey{}).(string)
	return dir
}
