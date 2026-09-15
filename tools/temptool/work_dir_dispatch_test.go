package temptool

// End-to-end: a TOOLBOX action carrying work_dir must actually START in the
// resolved folder. splitWorkDir is unit-tested next door; this asks the
// question the unit test cannot, which is whether the value survives the
// toolbox → synthetic-shell-tool → sandbox path and reaches the process.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// workDirTestWorkspace makes a workspace OUTSIDE /tmp.
//
// bwrapArgv mounts `--tmpfs /tmp` after binding the workspace, so a workspace
// under /tmp is covered by it and bwrap dies with "Can't chdir" — which in a
// cwd test looks exactly like the cwd being wrong. Same reason
// limitTestWorkspace exists in core/sandbox; production workspaces live under
// WorkspacesDir, not /tmp.
func workDirTestWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "gohort-workdir-")
	if err != nil {
		t.Skipf("no writable dir outside /tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestToolboxActionStartsInItsWorkDir(t *testing.T) {
	// A folder the command should start in, distinct from the workspace.
	bundle := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bundle, "node-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := workDirTestWorkspace(t)

	RegisterPathScope("wdtest", PathScope{
		Resolve: func(user, root, value string) (string, error) {
			if value == "." || value == "" {
				return filepath.EvalSymlinks(bundle)
			}
			return filepath.EvalSymlinks(filepath.Join(bundle, value))
		},
	})

	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		DB:           &DBase{Store: kvlite.MemStore()},
		WorkspaceDir: ws,
	}
	tool := &TempTool{
		Name: "wdbox", Description: "box", Mode: TempToolModeToolbox,
		Actions: []TempToolAction{{
			Name:            "where",
			Description:     "prints its working directory",
			CommandTemplate: "pwd",
			WorkDir:         "folder",
			Params: map[string]ToolParam{
				"folder": {Type: "string", PathScope: "wdtest:any"},
			},
			Required: []string{"folder"},
		}},
	}
	if err := sess.AppendTempTool(tool); err != nil {
		t.Fatalf("inject: %v", err)
	}

	out, err := dispatchTempToolUncached(sess, tool, map[string]any{
		"action": "where", "folder": "node-a",
	})
	if err != nil {
		t.Fatalf("dispatch: %v (out=%q)", err, out)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(bundle, "node-a"))
	got := strings.TrimSpace(out)
	if got != want {
		t.Errorf("the command started in the wrong directory\n got %q\nwant %q\n(workspace was %q)", got, want, ws)
	}
}

// The control: no work_dir, and the command still starts in the workspace.
func TestToolboxActionWithoutWorkDirStartsInTheWorkspace(t *testing.T) {
	ws := workDirTestWorkspace(t)
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		DB:           &DBase{Store: kvlite.MemStore()},
		WorkspaceDir: ws,
	}
	tool := &TempTool{
		Name: "wdbox2", Description: "box", Mode: TempToolModeToolbox,
		Actions: []TempToolAction{{
			Name: "where", Description: "prints cwd", CommandTemplate: "pwd",
		}},
	}
	if err := sess.AppendTempTool(tool); err != nil {
		t.Fatalf("inject: %v", err)
	}
	out, err := dispatchTempToolUncached(sess, tool, map[string]any{"action": "where"})
	if err != nil {
		t.Fatalf("dispatch: %v (out=%q)", err, out)
	}
	want, _ := filepath.EvalSymlinks(ws)
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("want the workspace %q, got %q", want, got)
	}
}
