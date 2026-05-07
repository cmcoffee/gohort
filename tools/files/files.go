// Package files provides sandboxed file-I/O tools — read_file,
// list_directory, write_file. All paths are resolved against the calling
// session's WorkspaceDir; absolute paths and `..` traversal are rejected
// hard. write_file requires explicit user confirmation per call.
//
// These tools satisfy the same role as OpenClaw-style "agent has hands"
// file access, but scoped to a per-user sandbox so a confused or
// misdirected agent can't reach into the rest of the host filesystem.
package files

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const (
	// maxReadBytes caps how much a single read_file returns. Beyond this
	// the call truncates and tells the LLM to use a more targeted
	// approach (head/tail/grep via run_local) for the next chunk.
	maxReadBytes = 64 * 1024

	// maxListEntries caps directory listings. Keeps very large dirs from
	// blowing up the LLM's context window.
	maxListEntries = 200

	// maxWriteBytes caps a single write_file payload. Anything bigger
	// should be staged via a series of writes or a script.
	maxWriteBytes = 256 * 1024
)

// Individual tools (ReadFileTool, ListDirectoryTool, WriteFileTool)
// are no longer registered — the consolidated `local` grouped tool
// (registered in local_grouped.go) covers all three plus delete + run.
// Their implementations remain so local_grouped.go's dispatchers can
// call them; just dropped from the catalog.
func init() {}

// --- read_file ---

type ReadFileTool struct{}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Caps() []Capability { return []Capability{CapRead} }
func (t *ReadFileTool) Desc() string {
	return "Read a file from your workspace sandbox. Path is relative to the workspace root; absolute paths and `..` traversal are rejected. Returns up to 64 KB of content; larger files are truncated. Binary files are returned as best-effort UTF-8 with replacement characters — use a more specific tool if you need raw bytes."
}
func (t *ReadFileTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"path": {Type: "string", Description: "Workspace-relative path to the file to read."},
	}
}
func (t *ReadFileTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("read_file requires a session with WorkspaceDir set")
}
func (t *ReadFileTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("read_file requires a session with WorkspaceDir set")
	}
	rel, _ := args["path"].(string)
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; use list_directory", rel)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	if len(data) > maxReadBytes {
		truncated := string(data[:maxReadBytes])
		return truncated + fmt.Sprintf("\n... [TRUNCATED: read %d of %d bytes — re-call with a smaller range or pipe via run_local for the rest]", maxReadBytes, len(data)), nil
	}
	return string(data), nil
}

// --- list_directory ---

type ListDirectoryTool struct{}

func (t *ListDirectoryTool) Name() string { return "list_directory" }
func (t *ListDirectoryTool) Caps() []Capability { return []Capability{CapRead} }
func (t *ListDirectoryTool) Desc() string {
	return "List the contents of a directory inside your workspace sandbox. Path is relative to the workspace root (use empty string for the root itself). Each line shows: type-flag (d/f/l), size in bytes, and name. Capped at 200 entries — larger dirs are truncated."
}
func (t *ListDirectoryTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"path": {Type: "string", Description: "Workspace-relative path to the directory. Empty string lists the workspace root."},
	}
}
func (t *ListDirectoryTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("list_directory requires a session with WorkspaceDir set")
}
func (t *ListDirectoryTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("list_directory requires a session with WorkspaceDir set")
	}
	rel, _ := args["path"].(string)
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("readdir %q: %w", rel, err)
	}
	// Sort: directories first, then files, alphabetical within each.
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return entries[i].Name() < entries[j].Name()
	})
	var lines []string
	for i, e := range entries {
		if i >= maxListEntries {
			lines = append(lines, fmt.Sprintf("... [TRUNCATED: %d more entries not shown]", len(entries)-i))
			break
		}
		flag := "f"
		if e.IsDir() {
			flag = "d"
		} else if e.Type()&os.ModeSymlink != 0 {
			flag = "l"
		}
		size := int64(0)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		lines = append(lines, fmt.Sprintf("%s\t%d\t%s", flag, size, e.Name()))
	}
	if len(lines) == 0 {
		return "(empty directory)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// --- write_file ---

type WriteFileTool struct{}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Caps() []Capability { return []Capability{CapWrite} }

// NeedsConfirm forces user approval for every write — the user sees the
// path and (truncated) content before bytes hit disk.
func (t *WriteFileTool) NeedsConfirm() bool { return true }

func (t *WriteFileTool) Desc() string {
	return "Write content to a file in your workspace sandbox. Path is relative to the workspace root; parent directories are created automatically. Mode controls existing-file behavior: \"create\" fails if the file exists, \"overwrite\" replaces it, \"append\" adds to the end. Each call requires explicit user approval. Single-call payload is capped at 256 KB — larger writes should be split."
}

func (t *WriteFileTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"path":    {Type: "string", Description: "Workspace-relative path to write."},
		"content": {Type: "string", Description: "The content to write to the file."},
		"mode":    {Type: "string", Description: "One of: \"create\" (fail if exists), \"overwrite\" (replace), \"append\" (add to end). Default is \"overwrite\".", Enum: []string{"create", "overwrite", "append"}},
	}
}

func (t *WriteFileTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("write_file requires a session with WorkspaceDir set")
}

func (t *WriteFileTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if _, err := EnsureSessionWorkspace(sess); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	rel, _ := args["path"].(string)
	content, _ := args["content"].(string)
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "overwrite"
	}
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(content) > maxWriteBytes {
		return "", fmt.Errorf("content too large: %d bytes (cap %d)", len(content), maxWriteBytes)
	}

	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", err
	}
	// Reject writes to anything that exists as a directory.
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return "", fmt.Errorf("%q is a directory", rel)
	}
	// Auto-create parent directories.
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}

	switch mode {
	case "create":
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return "", fmt.Errorf("create %q: %w", rel, err)
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return "", fmt.Errorf("write: %w", err)
		}
	case "append":
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return "", fmt.Errorf("open for append %q: %w", rel, err)
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return "", fmt.Errorf("append: %w", err)
		}
	case "overwrite":
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("write %q: %w", rel, err)
		}
	default:
		return "", fmt.Errorf("invalid mode %q (use create | overwrite | append)", mode)
	}
	// In-band next-step hint. Kept as plain prose without tool-call
	// syntax so PromptTools-mode parsers don't mistake the example
	// for a real call shape and so models like Qwen don't echo the
	// placeholder back as visible chat content. The bare verbs are
	// enough — the model already has the catalog and knows the
	// argument shape; this just nudges intent.
	return fmt.Sprintf(
		"wrote %d bytes to %s (mode=%s). If this is executable code, run it to test — or define a reusable tool that wraps it. If it is data or output, no further action is needed.",
		len(content), rel, mode,
	), nil
}
