package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-user workspace directories isolate the local-exec and file-I/O tools'
// side effects to a specific filesystem subtree. Even if the LLM goes
// off-script, it can't reach outside its sandbox: paths are resolved with
// hard validation that rejects `..` traversal and absolute paths, and
// shell tools run with the workspace as cwd.
//
// Layout: <workspaces-dir>/<userID>/
// where workspaces-dir is set once at startup via SetWorkspacesDir.

var (
	workspacesDirMu sync.RWMutex
	workspacesDir   string
)

// SetWorkspacesDir configures the base directory under which per-user
// workspaces are created. Call once at startup before any sandboxed tool
// runs. Typically wired in init_database alongside SetImageDir.
func SetWorkspacesDir(dir string) {
	workspacesDirMu.Lock()
	workspacesDir = dir
	workspacesDirMu.Unlock()
}

// WorkspacesDir returns the configured base. Empty if SetWorkspacesDir
// hasn't been called — callers should treat that as "sandboxed tools
// disabled" rather than fall back to a default that might escape control.
func WorkspacesDir() string {
	workspacesDirMu.RLock()
	defer workspacesDirMu.RUnlock()
	return workspacesDir
}

// EnsureWorkspaceDir computes the absolute workspace directory for userID,
// creating it (with parents) if it doesn't exist yet. Returns the absolute
// path. userID must be non-empty and free of path-separator characters
// (rejected to prevent escape via crafted IDs).
func EnsureWorkspaceDir(userID string) (string, error) {
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured (SetWorkspacesDir not called)")
	}
	if userID == "" {
		return "", fmt.Errorf("userID required for workspace resolution")
	}
	if strings.ContainsAny(userID, `/\`) || strings.Contains(userID, "..") || userID == "." {
		return "", fmt.Errorf("invalid userID: %q", userID)
	}
	dir := filepath.Join(base, userID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}
	return dir, nil
}

// ResolveWorkspacePath joins root and rel, then verifies the result lies
// inside root. Rejects absolute rel paths, `..` traversal, and any clever
// symlink-ish combinations that might escape the sandbox.
//
// Returns the absolute, cleaned path on success. The file/dir at that
// path may or may not exist — callers Stat as needed.
func ResolveWorkspacePath(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("workspace root not set")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return root, nil // caller wants the root itself
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed: %q", rel)
	}
	// Reject `..` segments before Join can resolve them past the root.
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal not allowed: %q", rel)
		}
	}
	joined := filepath.Join(root, rel)
	cleaned := filepath.Clean(joined)
	cleanedRoot := filepath.Clean(root)
	// Final containment check: cleaned must be the root itself or live
	// inside it. Compare with a separator suffix on the root so a
	// `<root>foo/bar` sibling can't pass.
	if cleaned != cleanedRoot && !strings.HasPrefix(cleaned, cleanedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace: %q", rel)
	}
	// Symlink-walk check: every component INSIDE the workspace must not
	// be a symlink. Without this the path-string sandbox is bypassable
	// by planting a symlink — `read_file("escape/passwd")` would pass
	// the prefix check but `os.Open` would follow the link out. Symlinks
	// AT or ABOVE the workspace root (e.g. /home → /var/users) are fine;
	// we only care about links the agent itself could have created.
	if err := validateNoSymlinks(cleanedRoot, cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
}

// validateNoSymlinks walks every path component between root (exclusive)
// and abs (inclusive) and rejects if any existing component is a symlink.
// Non-existent components are skipped — write_file creating a new file
// is fine; the check only catches paths that traverse a planted link.
func validateNoSymlinks(root, abs string) error {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return fmt.Errorf("relpath: %w", err)
	}
	if rel == "." {
		return nil // the root itself; we don't lstat it (may be a legit symlink set up by the operator)
	}
	cur := root
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		if p == "" {
			continue
		}
		cur = filepath.Join(cur, p)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // not yet created — no symlink to follow
			}
			return fmt.Errorf("lstat %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path traverses a symlink at %q — not allowed inside the workspace sandbox", cur)
		}
	}
	return nil
}
