package temptool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// linkFixture is a workspace and, beside it, a folder standing for one the
// server can write and a sandbox should never reach (the shared library every
// sandbox imports, say).
func linkFixture(t *testing.T) (ws, outside string) {
	t.Helper()
	base := t.TempDir()
	ws, outside = filepath.Join(base, "ws"), filepath.Join(base, "_gohort_lib")
	os.MkdirAll(ws, 0o700)
	os.MkdirAll(outside, 0o700)
	return ws, outside
}

// A script's name is predictable, so a script could leave a link at the name
// its next tool's script will be written to. The write lands in the workspace,
// replacing the link, and never where the link pointed.
func TestAToolsScriptIsNeverWrittenThroughALink(t *testing.T) {
	ws, outside := linkFixture(t)
	body := "print('hi')\n"
	canonical := canonicalScriptName("mytool", "mytool.py", body)
	target := filepath.Join(outside, "sitecustomize.py")
	if err := os.Symlink(target, filepath.Join(ws, canonical)); err != nil {
		t.Skip("no symlinks here")
	}
	sess := &ToolSession{WorkspaceDir: ws}
	if _, _, err := deployScriptBody(sess, "mytool", "mytool.py", body); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the script was written through the link, outside the workspace")
	}
	info, err := os.Lstat(filepath.Join(ws, canonical))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the script should be a plain file in the workspace: %v %v", info, err)
	}
}

// A tool folder swapped for a link out of the workspace is not written through.
func TestAToolFolderIsNeverWrittenThroughALink(t *testing.T) {
	ws, outside := linkFixture(t)
	if err := os.Symlink(outside, filepath.Join(ws, "mytool.tool")); err != nil {
		t.Skip("no symlinks here")
	}
	tt := TempTool{Name: "mytool", Mode: TempToolModeShell, ScriptName: "mytool.py", ScriptBody: "print(1)\n", CommandTemplate: "python3 {workspace_dir}/mytool.py"}
	if _, err := writeToolFolder(ws, filepath.Join(ws, "mytool.tool"), tt, ""); err == nil {
		t.Error("wrote a tool folder through a link")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("files landed outside the workspace: %v", entries)
	}
}

// A value too large for the environment is written to a file in the
// workspace, never through a link a script left where that file goes.
func TestALargeArgumentIsNeverWrittenThroughALink(t *testing.T) {
	ws, outside := linkFixture(t)
	if err := os.Symlink(outside, filepath.Join(ws, ".tool_args")); err != nil {
		t.Skip("no symlinks here")
	}
	tt := &TempTool{Name: "mytool"}
	env := map[string]string{"BODY": strings.Repeat("x", maxEnvArgBytes+1)}
	if _, err := passLargeArgs(tt, map[string]any{}, env, &ToolSession{WorkspaceDir: ws}, ws); err == nil {
		t.Error("wrote a large argument through a link")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("files landed outside the workspace: %v", entries)
	}
}
