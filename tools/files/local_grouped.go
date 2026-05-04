// `local` — grouped tool consolidating filesystem + sandboxed shell
// operations into a single catalog entry. Replaces the cognitive
// overhead of separately scanning read_file / write_file /
// list_directory / run_local descriptions every round; the LLM picks
// `local` then chooses an action.
//
// Actions: read | write | delete | list | run
// All operations are workspace-sandboxed via ResolveWorkspacePath +
// (for run) RunSandboxedShell. Same security posture as the
// individual tools — this is a UX consolidation, not a relaxation
// of any gate.

package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	localGroupedRunTimeout = 90 * time.Second
	localGroupedMaxOutput  = 10000
)

func init() {
	gt := NewGroupedTool("local",
		"Workspace-sandboxed filesystem + shell operations. The workspace is the only writable path; reads/writes/exec outside it are rejected at the resolution layer. Network is allowed inside the sandbox.")

	gt.AddAction("read", &GroupedToolAction{
		Description: "Read a file from your workspace as text. Returns up to 50KB; longer files truncated with a notice.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative path to read."},
		},
		Required: []string{"path"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			t := &ReadFileTool{}
			return t.RunWithSession(args, sess)
		},
	})

	gt.AddAction("write", &GroupedToolAction{
		Description: "Write content to a workspace file (creates or overwrites). Pair with attach_file to deliver the result, or with run for executable scripts.",
		Params: map[string]ToolParam{
			"path":    {Type: "string", Description: "Workspace-relative path."},
			"content": {Type: "string", Description: "Bytes to write."},
		},
		Required:     []string{"path", "content"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			t := &WriteFileTool{}
			return t.RunWithSession(args, sess)
		},
	})

	gt.AddAction("delete", &GroupedToolAction{
		Description: "Delete a workspace file. Workspace-only; rejects paths outside.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative path to delete."},
		},
		Required:     []string{"path"},
		Caps:         []Capability{CapWrite},
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return localDelete(args, sess)
		},
	})

	gt.AddAction("list", &GroupedToolAction{
		Description: "List entries in a workspace directory. Use \".\" for the workspace root.",
		Params: map[string]ToolParam{
			"path": {Type: "string", Description: "Workspace-relative directory path. Default \".\""},
		},
		Required: nil,
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			t := &ListDirectoryTool{}
			return t.RunWithSession(args, sess)
		},
	})

	gt.AddAction("run", &GroupedToolAction{
		Description: "Run a shell command in the workspace via the bwrap sandbox (when available). The command sees only the workspace as writable; reads outside the workspace silently fail. Network is allowed. 90s timeout, output capped at 10KB.",
		Params: map[string]ToolParam{
			"command": {Type: "string", Description: "Shell command to execute. Standard sh -c semantics — pipes, redirects, quoting work normally."},
		},
		Required:     []string{"command"},
		Caps:         []Capability{CapExecute, CapRead, CapWrite, CapNetwork},
		NeedsConfirm: true,
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return localRun(args, sess)
		},
	})

	RegisterChatTool(gt)
}

// localDelete removes a workspace file, validating the path first.
func localDelete(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("delete requires a session with WorkspaceDir set")
	}
	rel := strings.TrimSpace(StringArg(args, "path"))
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; use shell rm -r if you really need to remove a directory", rel)
	}
	if err := os.Remove(abs); err != nil {
		return "", fmt.Errorf("remove %q: %w", rel, err)
	}
	return fmt.Sprintf("Deleted %s.", filepath.Base(rel)), nil
}

// localRun is a thin wrapper around RunSandboxedShell with the same
// timeout + output cap as the standalone run_local tool.
func localRun(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("run requires a session with WorkspaceDir set")
	}
	cmd := strings.TrimSpace(StringArg(args, "command"))
	if cmd == "" {
		return "", fmt.Errorf("command is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), localGroupedRunTimeout)
	defer cancel()
	res := RunSandboxedShell(ctx, cmd, sess.WorkspaceDir)
	output := strings.TrimSpace(res.Output)
	if len(output) > localGroupedMaxOutput {
		totalLines := strings.Count(output, "\n") + 1
		truncated := output[:localGroupedMaxOutput]
		shown := strings.Count(truncated, "\n") + 1
		output = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars).]",
			shown, totalLines, len(output))
	}
	if res.TimedOut {
		notice := fmt.Sprintf("\n[TIMED OUT after %s — command killed.]", localGroupedRunTimeout)
		if output == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return output + notice, nil
	}
	if res.Err != nil {
		if output == "" {
			return fmt.Sprintf("[exit: %v — no output]", res.Err), nil
		}
		return output + fmt.Sprintf("\n[exit: %v]", res.Err), nil
	}
	return output, nil
}
