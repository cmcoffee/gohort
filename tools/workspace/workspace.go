// LLM-facing workspace management. Lets the LLM mint scratch
// directories for a task, switch the session's active workspace to
// one, pin them to survive session end, list / inspect / delete.
//
// Mental model: ephemeral by default ("with TemporaryDirectory()"),
// pinned when the LLM says "actually, save this for later." Auto-
// cleaned by the core reconciler when unpinned and idle > 24h.

package workspace

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	gt := NewGroupedTool("workspace",
		"Manage scratch directories for tasks. Create one when you need a place to drop generated images, downloaded videos, fetched files, or work products. Default: ephemeral — auto-cleaned 24h after last use. Pin to keep it around across sessions. Most apps already give you a default workspace; use these for isolated task scratch space when you don't want to mix files into the long-lived workspace.")

	gt.AddAction("create", &GroupedToolAction{
		Description: "Create a new workspace and switch the session's active path to it. All subsequent file-writing tools (fetch_url save_to, generate_image, local file write, etc.) put their output here for the rest of this turn. Returns the workspace id (use it later for action=use, pin, delete, info). Optional: pin=true creates it pinned (survives session end + idle timeout); name is a human-friendly label.",
		Params: map[string]ToolParam{
			"name": {Type: "string", Description: "Human-friendly label for the workspace (snake_case recommended). Optional but useful for `list` and admin views."},
			"pin":  {Type: "boolean", Description: "If true, creates the workspace as pinned — won't be auto-cleaned. Default false (ephemeral)."},
		},
		Caps:    []Capability{CapWrite},
		Handler: handleCreate,
	})

	gt.AddAction("use", &GroupedToolAction{
		Description: "Switch the session's active workspace to an existing one. Subsequent file tools write here for the rest of this turn. Touches the workspace's last-used time so the cleanup reconciler doesn't reap it.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID (from list or create)."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  handleUse,
	})

	gt.AddAction("list", &GroupedToolAction{
		Description: "List your workspaces (ephemeral + pinned). Shows id, name, pinned status, size, last-used age.",
		Params:      map[string]ToolParam{},
		Caps:        []Capability{CapRead},
		Handler:     handleList,
	})

	gt.AddAction("pin", &GroupedToolAction{
		Description: "Mark a workspace as pinned — survives session end and is not auto-cleaned by the idle reconciler. Use when you want to keep work product around for a future session.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setPinned(args, sess, true) },
	})

	gt.AddAction("unpin", &GroupedToolAction{
		Description: "Revert a workspace to ephemeral. The cleanup reconciler will reap it after 24h idle.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler:  func(args map[string]any, sess *ToolSession) (string, error) { return setPinned(args, sess, false) },
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Permanently delete a workspace and all its files. Irreversible. Use when you're done with a task and don't want to wait for the cleanup reconciler.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required:     []string{"id"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler:      handleDelete,
	})

	gt.AddAction("info", &GroupedToolAction{
		Description: "Detailed info on one workspace: metadata, on-disk size, age. Useful before deciding whether to delete.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Workspace ID."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler:  handleInfo,
	})

	RegisterChatTool(gt)
}

// ----------------------------------------------------------------------
// handlers
// ----------------------------------------------------------------------

func handleCreate(args map[string]any, sess *ToolSession) (string, error) {
	owner := ownerFor(sess)
	if owner == "" {
		return "", fmt.Errorf("managed workspaces require an authenticated session (no owner)")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	pin := boolArg(args, "pin")
	w, dir, err := CreateManagedWorkspace(name, owner, sessionIDFor(sess), pin)
	if err != nil {
		return "", err
	}
	// Switch the session's active workspace dir to the new one for
	// the remainder of this turn. Tools that resolve paths against
	// sess.WorkspaceDir will land here automatically. WorkspaceID is
	// captured so tool_def(persist=true) can bind the tool to this
	// workspace at create time.
	if sess != nil {
		sess.WorkspaceDir = dir
		sess.WorkspaceID = w.ID
	}
	pinNote := ""
	if w.Pinned {
		pinNote = " (PINNED — survives session end)"
	}
	return fmt.Sprintf("Workspace created (id=%s, name=%q)%s. Active for this session — file-writing tools will land here. Path: %s",
		w.ID, w.Name, pinNote, dir), nil
}

func handleUse(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	dir, err := ManagedWorkspaceDir(w.Owner, w.ID)
	if err != nil {
		return "", err
	}
	if sess != nil {
		sess.WorkspaceDir = dir
		sess.WorkspaceID = w.ID
	}
	TouchManagedWorkspace(id)
	return fmt.Sprintf("Switched active workspace to %q (id=%s, path=%s).", w.Name, w.ID, dir), nil
}

func handleList(args map[string]any, sess *ToolSession) (string, error) {
	owner := ownerFor(sess)
	if owner == "" {
		return "", fmt.Errorf("workspaces require an authenticated session")
	}
	ws := ListManagedWorkspaces(owner)
	if len(ws) == 0 {
		return "No workspaces — use action=create to mint one.", nil
	}
	sort.Slice(ws, func(i, j int) bool {
		// Pinned first, then most-recently-used.
		if ws[i].Pinned != ws[j].Pinned {
			return ws[i].Pinned
		}
		return ws[i].LastUsedAt.After(ws[j].LastUsedAt)
	})
	var b strings.Builder
	for _, w := range ws {
		size := ManagedWorkspaceSize(w.Owner, w.ID)
		state := "ephemeral"
		if w.Pinned {
			state = "PINNED"
		}
		idle := time.Since(w.LastUsedAt).Round(time.Minute)
		fmt.Fprintf(&b, "- id=%s  name=%q  %s  size=%dKB  idle=%s\n",
			w.ID, w.Name, state, size/1024, idle)
	}
	return b.String(), nil
}

func setPinned(args map[string]any, sess *ToolSession, pinned bool) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	if err := SetManagedWorkspacePinned(id, pinned); err != nil {
		return "", err
	}
	state := "ephemeral"
	if pinned {
		state = "pinned"
	}
	return fmt.Sprintf("Workspace %q is now %s.", w.Name, state), nil
}

func handleDelete(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	if err := DeleteManagedWorkspace(id); err != nil {
		return "", err
	}
	// If the deleted workspace was the session's active path, blank it
	// so subsequent tool calls don't try to write into a missing dir.
	dir, _ := ManagedWorkspaceDir(w.Owner, w.ID)
	if sess != nil && sess.WorkspaceDir == dir {
		sess.WorkspaceDir = ""
	}
	return fmt.Sprintf("Workspace %q deleted.", w.Name), nil
}

func handleInfo(args map[string]any, sess *ToolSession) (string, error) {
	id := strings.TrimSpace(StringArg(args, "id"))
	w, ok := LoadManagedWorkspace(id)
	if !ok {
		return "", fmt.Errorf("workspace %q not found", id)
	}
	if w.Owner != ownerFor(sess) {
		return "", fmt.Errorf("workspace %q is not yours", id)
	}
	size := ManagedWorkspaceSize(w.Owner, w.ID)
	dir, _ := ManagedWorkspaceDir(w.Owner, w.ID)
	out, _ := json.MarshalIndent(map[string]any{
		"id":           w.ID,
		"name":         w.Name,
		"owner":        w.Owner,
		"pinned":       w.Pinned,
		"session_id":   w.SessionID,
		"created_at":   w.CreatedAt.UTC().Format(time.RFC3339),
		"last_used_at": w.LastUsedAt.UTC().Format(time.RFC3339),
		"size_bytes":   size,
		"path":         dir,
	}, "", "  ")
	return string(out), nil
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func ownerFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	return sess.Username
}

// sessionIDFor extracts a stable session identifier from the ToolSession.
// Prefers ChatSessionID (chat app) then RoutingTarget (for phantom convs);
// returns empty when neither is set, in which case the workspace is
// treated as cross-session by default.
func sessionIDFor(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	if sess.ChatSessionID != "" {
		return "chat:" + sess.ChatSessionID
	}
	if sess.RoutingTarget != "" {
		return sess.RoutingTarget
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	}
	return false
}
