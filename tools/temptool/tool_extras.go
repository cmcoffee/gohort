package temptool

// Notes and helper files on a tool, whatever its mode.
//
// Each mode builds its record its own way, so these two are applied once the
// mode's create has run: on the session draft it registered, which is then
// persisted again where the first finalize put it. An update re-runs create
// with the stored record's fields, so both have to ride through there too, or
// an edit would quietly drop them.

import (
	"fmt"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// carriedWorkspaceFiles is the create-args key an update uses for the helper
// files the stored record already had: applied only when create found none
// of its own in the workspace, so a fresher copy on disk still wins.
const carriedWorkspaceFiles = "_carried_workspace_files"

// maxToolNotes bounds a tool's notes: room for what the next editor needs,
// not a manual.
const maxToolNotes = 16 << 10

// maxWorkspaceFileBytes bounds the helper files one tool carries.
const maxWorkspaceFileBytes = 512 << 10

// workspaceFilesArg reads [{path, content, mode?}], refusing a path that is
// not one flat filename (they are deployed under that literal name).
func workspaceFilesArg(raw any) ([]RecipeFile, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]RecipeFile); ok {
			return typed, nil
		}
		return nil, fmt.Errorf("workspace_files is a list of {path, content}")
	}
	var out []RecipeFile
	total := 0
	for _, e := range list {
		m, _ := e.(map[string]any)
		path := strings.TrimSpace(StringArg(m, "path"))
		content, _ := m["content"].(string)
		if path == "" || strings.ContainsAny(path, `/\`) || path == "." || path == ".." || strings.HasPrefix(path, ".") {
			return nil, fmt.Errorf("workspace_files: %q is not a plain filename (helpers are deployed beside the script under their own name)", path)
		}
		total += len(content)
		mode := uint32(0)
		if f, ok := m["mode"].(float64); ok {
			mode = uint32(f)
		}
		out = append(out, RecipeFile{Path: path, Content: content, Mode: mode})
	}
	if len(out) > maxWorkspaceHelpers {
		return nil, fmt.Errorf("workspace_files: %d files, over the %d a tool may carry", len(out), maxWorkspaceHelpers)
	}
	if total > maxWorkspaceFileBytes {
		return nil, fmt.Errorf("workspace_files: %d bytes, over the %d a tool may carry", total, maxWorkspaceFileBytes)
	}
	return out, nil
}

// recipeFilesToArgs is the create-args shape of a record's helper files.
func recipeFilesToArgs(files []RecipeFile) []any {
	out := make([]any, 0, len(files))
	for _, f := range files {
		m := map[string]any{"path": f.Path, "content": f.Content}
		if f.Mode != 0 {
			m["mode"] = float64(f.Mode)
		}
		out = append(out, m)
	}
	return out
}

// applyToolExtras sets notes and helper files on the tool create just made,
// and persists it again where finalize first put it. files are given ones
// (they replace whatever create gathered); carried are an update's stored
// ones (used only when create gathered none).
func applyToolExtras(sess *ToolSession, name string, args map[string]any, files, carried []RecipeFile) {
	notes, hasNotes := args["notes"].(string)
	if sess == nil || name == "" || (!hasNotes && files == nil && carried == nil) {
		return
	}
	var draft *TempTool
	for _, d := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
		if d.Name == name {
			tmp := d
			draft = &tmp
			break
		}
	}
	live := sess.LookupTempTool(name)
	if draft == nil && live == nil {
		return
	}
	apply := func(t *TempTool) {
		if hasNotes {
			t.Notes = truncateNotes(notes)
		}
		switch {
		case files != nil:
			t.WorkspaceFiles = files
		case len(t.WorkspaceFiles) == 0 && carried != nil:
			t.WorkspaceFiles = carried
		}
	}
	if live != nil {
		// A copy swapped in, never the session's own record written in place:
		// a dispatch may be reading it.
		cp := *live
		apply(&cp)
		sess.RemoveTempTool(name)
		if err := sess.AppendTempTool(&cp); err != nil {
			Log("[tool_def] %q: could not refresh the session copy with its notes and helpers: %v", name, err)
		}
	}
	if draft != nil {
		apply(draft)
		SaveSessionTempTool(sess.DB, sess.ChatSessionID, *draft)
		finalizeAuthoredTool(sess, name)
	}
}

// truncateNotes bounds notes, saying so where it cut.
func truncateNotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxToolNotes {
		return s
	}
	return s[:maxToolNotes] + "\n\n(notes cut at " + fmt.Sprint(maxToolNotes) + " bytes)"
}

// cleanBase is a filename with any directory dropped.
func cleanBase(p string) string { return filepath.Base(filepath.Clean(p)) }
