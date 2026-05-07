// Managed workspaces — task-scoped scratch spaces the LLM creates and
// destroys at runtime. Distinct from the per-user default workspace
// (which is always-on, owned by the app), these are short-lived and
// typically auto-clean when their originating session ends.
//
// Mental model: `with tempfile.TemporaryDirectory() as d:` in Python.
// The LLM creates one when it needs scratch space for a task, files
// pile up there, then it explicitly destroys it OR pins it to keep
// it around. Unpinned workspaces auto-clean after 24h idle so a
// crashed agent doesn't leak state.
//
// Layout: <workspaces-dir>/<owner>/_ws/<workspace-id>/
// Storage: managed_workspaces table on AuthDB() — record per workspace.

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managedWorkspaceTable     = "managed_workspaces"
	managedWorkspaceSubdir    = "_ws"
	managedWorkspaceIdleLimit = 24 * time.Hour
)

// ManagedWorkspace is the persistent record for an LLM-created
// workspace. The actual files live on disk under managedWorkspaceDir().
type ManagedWorkspace struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Owner      string    `json:"owner"`
	Pinned     bool      `json:"pinned"`
	SessionID  string    `json:"session_id,omitempty"` // origin chat/phantom session; empty for detached pinned
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

func managedWorkspaceDB() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// ManagedWorkspaceDir returns the on-disk path for the given owner +
// workspace ID, creating intermediate directories. Validates the ID
// shape so a crafted ID can't escape the owner subtree.
func ManagedWorkspaceDir(owner, wsID string) (string, error) {
	if owner == "" {
		return "", fmt.Errorf("owner required")
	}
	if wsID == "" {
		return "", fmt.Errorf("workspace id required")
	}
	if strings.ContainsAny(wsID, `/\`) || strings.Contains(wsID, "..") {
		return "", fmt.Errorf("invalid workspace id: %q", wsID)
	}
	base := WorkspacesDir()
	if base == "" {
		return "", fmt.Errorf("workspaces dir not configured")
	}
	dir := filepath.Join(base, owner, managedWorkspaceSubdir, wsID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	return dir, nil
}

// CreateManagedWorkspace mints a new ephemeral (or pinned) workspace
// owned by `owner`, originating from `sessionID` (which may be empty
// for non-session contexts). Returns the record + the absolute path
// the caller should hand to tools as their WorkspaceDir.
func CreateManagedWorkspace(name, owner, sessionID string, pinned bool) (ManagedWorkspace, string, error) {
	if owner == "" {
		return ManagedWorkspace{}, "", fmt.Errorf("owner required (managed workspaces are owner-scoped)")
	}
	db := managedWorkspaceDB()
	if db == nil {
		return ManagedWorkspace{}, "", fmt.Errorf("managed workspace store not initialized")
	}
	w := ManagedWorkspace{
		ID:         UUIDv4(),
		Name:       strings.TrimSpace(name),
		Owner:      owner,
		Pinned:     pinned,
		SessionID:  sessionID,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
	}
	dir, err := ManagedWorkspaceDir(owner, w.ID)
	if err != nil {
		return ManagedWorkspace{}, "", err
	}
	db.Set(managedWorkspaceTable, w.ID, w)
	return w, dir, nil
}

// EnsureSessionWorkspace guarantees the session has an active
// workspace. If sess.WorkspaceDir is already set, it is returned
// unchanged. Otherwise an unnamed ephemeral managed workspace is
// minted, the session's WorkspaceDir/WorkspaceID are populated, and
// the new dir is returned.
//
// Used by tools that need a sandbox to operate in (local read/write/
// run, tool_def create with mode=shell) so the LLM never has to
// explicitly mint a workspace before its first useful call.
//
// Requires sess.Username to be set — managed workspaces are
// owner-scoped. ChatSessionID / RoutingTarget are used as the
// originating session ID when available.
func EnsureSessionWorkspace(sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("session required")
	}
	if sess.WorkspaceDir != "" {
		return sess.WorkspaceDir, nil
	}
	if sess.Username == "" {
		return "", fmt.Errorf("auto-mint requires an authenticated session (no owner)")
	}
	sessionID := ""
	switch {
	case sess.ChatSessionID != "":
		sessionID = "chat:" + sess.ChatSessionID
	case sess.RoutingTarget != "":
		sessionID = sess.RoutingTarget
	}
	w, dir, err := CreateManagedWorkspace("", sess.Username, sessionID, false)
	if err != nil {
		return "", err
	}
	sess.WorkspaceDir = dir
	sess.WorkspaceID = w.ID
	return dir, nil
}

// LoadManagedWorkspace fetches a workspace record by ID.
func LoadManagedWorkspace(id string) (ManagedWorkspace, bool) {
	db := managedWorkspaceDB()
	if db == nil || id == "" {
		return ManagedWorkspace{}, false
	}
	var w ManagedWorkspace
	ok := db.Get(managedWorkspaceTable, id, &w)
	return w, ok
}

// ListManagedWorkspaces returns workspaces filtered by owner. Empty
// owner returns all (admin view).
func ListManagedWorkspaces(owner string) []ManagedWorkspace {
	db := managedWorkspaceDB()
	if db == nil {
		return nil
	}
	var out []ManagedWorkspace
	for _, k := range db.Keys(managedWorkspaceTable) {
		var w ManagedWorkspace
		if !db.Get(managedWorkspaceTable, k, &w) {
			continue
		}
		if owner != "" && w.Owner != owner {
			continue
		}
		out = append(out, w)
	}
	return out
}

// DeleteManagedWorkspace removes the DB record AND wipes the directory.
// Idempotent — missing dir or record is treated as success.
func DeleteManagedWorkspace(id string) error {
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return nil
	}
	dir, dirErr := ManagedWorkspaceDir(w.Owner, w.ID)
	db := managedWorkspaceDB()
	if db != nil {
		db.Unset(managedWorkspaceTable, id)
	}
	if dirErr == nil {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// SetManagedWorkspacePinned toggles the pin flag.
func SetManagedWorkspacePinned(id string, pinned bool) error {
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return fmt.Errorf("workspace %q not found", id)
	}
	w.Pinned = pinned
	db := managedWorkspaceDB()
	if db == nil {
		return fmt.Errorf("managed workspace store not initialized")
	}
	db.Set(managedWorkspaceTable, id, w)
	return nil
}

// TouchManagedWorkspace bumps LastUsedAt so the cleanup reconciler
// doesn't delete an active workspace just because its owner-session
// happens to be quiet for a while.
func TouchManagedWorkspace(id string) {
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return
	}
	w.LastUsedAt = time.Now()
	if db := managedWorkspaceDB(); db != nil {
		db.Set(managedWorkspaceTable, id, w)
	}
}

// ManagedWorkspaceSize returns the on-disk byte total for a workspace.
// Walks the directory; cheap for typical sizes (a few hundred files
// at most), too slow if someone dumps GBs into a workspace — caller
// can cache or skip if needed.
func ManagedWorkspaceSize(owner, id string) int64 {
	dir, err := ManagedWorkspaceDir(owner, id)
	if err != nil {
		return 0
	}
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// init registers the cleanup reconciler so the scheduler periodically
// purges expired ephemeral workspaces. Pinned ones are never auto-
// deleted; only explicit DeleteManagedWorkspace removes them.
func init() {
	RegisterReconciler("managed_workspace_cleanup", func(ctx context.Context) error {
		ws := ListManagedWorkspaces("")
		now := time.Now()
		removed := 0
		for _, w := range ws {
			if w.Pinned {
				continue
			}
			idle := now.Sub(w.LastUsedAt)
			if idle < managedWorkspaceIdleLimit {
				continue
			}
			if err := DeleteManagedWorkspace(w.ID); err != nil {
				Log("[workspace] cleanup failed for %s: %v", w.ID, err)
				continue
			}
			removed++
			Debug("[workspace] cleaned up ephemeral workspace %s (name=%q, idle=%s)", w.ID, w.Name, idle.Round(time.Minute))
		}
		if removed > 0 {
			Log("[workspace] cleanup removed %d ephemeral workspace(s)", removed)
		}
		return nil
	})
}
