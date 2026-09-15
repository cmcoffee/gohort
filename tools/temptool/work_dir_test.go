package temptool

// A mapped command that must run INSIDE its folder. The folder is proved by a
// path scope, then carried as a working directory rather than as a scoped read
// — the distinction that keeps it working on a backend whose sandbox cannot
// scope a read.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func scopedFolderTool() *TempTool {
	return &TempTool{
		Name:            "weka",
		Mode:            TempToolModeShell,
		CommandTemplate: "/opt/bin/weka syshealth",
		WorkDir:         "folder",
		Params: map[string]ToolParam{
			"folder": {Type: "string", PathScope: "files:bundles"},
		},
	}
}

func TestWorkDirLeavesTheScopedReadList(t *testing.T) {
	tt := scopedFolderTool()
	args := map[string]any{"folder": "/srv/bundles/diag"}

	dir, scoped, err := splitWorkDir(tt, args, []string{"/srv/bundles/diag"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if dir != "/srv/bundles/diag" {
		t.Errorf("want the resolved folder as cwd, got %q", dir)
	}
	// The whole point: a working directory is not a promise that reads are
	// confined to it, so it must not travel as one. Left in, it would refuse
	// the run outright on any backend whose scopesReads() is false.
	if len(scoped) != 0 {
		t.Errorf("the work dir must not remain a scoped read: %v", scoped)
	}
}

// Other scoped paths are untouched — they ARE read promises.
func TestWorkDirKeepsOtherScopedPaths(t *testing.T) {
	tt := scopedFolderTool()
	tt.Params["corpus"] = ToolParam{Type: "string", PathScope: "files:bundles"}
	args := map[string]any{"folder": "/srv/bundles/diag", "corpus": "/srv/bundles/ref"}

	dir, scoped, err := splitWorkDir(tt, args, []string{"/srv/bundles/diag", "/srv/bundles/ref"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if dir != "/srv/bundles/diag" {
		t.Errorf("cwd: %q", dir)
	}
	if len(scoped) != 1 || scoped[0] != "/srv/bundles/ref" {
		t.Errorf("a real scoped read should survive: %v", scoped)
	}
}

// An unscoped work_dir parameter is the original bug: nothing resolves the
// model's value, so the cwd would be a folder NAME read against the workspace.
func TestWorkDirWithoutAPathScopeIsRefused(t *testing.T) {
	tt := scopedFolderTool()
	tt.Params["folder"] = ToolParam{Type: "string"}

	if _, _, err := splitWorkDir(tt, map[string]any{"folder": "diag"}, nil); err == nil {
		t.Fatal("an unscoped work_dir must be refused, not run against the workspace")
	}
}

func TestWorkDirNamingNoParameterIsRefused(t *testing.T) {
	tt := scopedFolderTool()
	tt.WorkDir = "nope"

	if _, _, err := splitWorkDir(tt, map[string]any{"folder": "/srv/bundles/diag"}, nil); err == nil {
		t.Fatal("work_dir naming an undeclared parameter must be refused")
	}
}

// A declared work_dir with no folder is REFUSED, never run in the workspace.
//
// This is the live failure that prompted it: weka ran in the agent's
// workspace and reported "no matching nodes found in .../workspaces/...",
// so the reader chased a path nobody had chosen instead of the missing
// argument that put it there. A silent fallback turns a missing argument into
// a wrong answer somewhere else.
func TestWorkDirOmittedOnACallIsRefused(t *testing.T) {
	tt := scopedFolderTool()
	_, _, err := splitWorkDir(tt, map[string]any{}, []string{"/srv/x"})
	if err == nil {
		t.Fatal("a declared work_dir with no folder must be refused")
	}
	if !strings.Contains(err.Error(), "folder") {
		t.Errorf("the refusal should name the missing parameter, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Nothing ran") {
		t.Errorf("it must be clear the command did not run: %v", err)
	}
}

// Every tool that declares nothing behaves exactly as before.
func TestNoWorkDirIsUntouched(t *testing.T) {
	tt := &TempTool{Name: "plain", Mode: TempToolModeShell, CommandTemplate: "echo hi"}
	dir, scoped, err := splitWorkDir(tt, map[string]any{}, []string{"/srv/x"})
	if err != nil || dir != "" || len(scoped) != 1 {
		t.Fatalf("unchanged path: dir=%q scoped=%v err=%v", dir, scoped, err)
	}
}

// liveRequired builds the schema the MODEL reads. It used to narrow a
// required list down to the URL PATH placeholders — a question a command line
// cannot answer — so every shell action reported all of its parameters
// optional while the dispatcher went on enforcing the stored list.
//
// A work_dir parameter is the worst case: deliberately absent from the
// command line, so narrowing against the command's own placeholders would
// drop it too.
func TestShellActionKeepsItsRequiredList(t *testing.T) {
	act := TempToolAction{
		Name:            "syshealth",
		CommandTemplate: "/opt/bin/weka syshealth",
		WorkDir:         "folder",
		Params:          map[string]ToolParam{"folder": {Type: "string", PathScope: "files:b"}},
		Required:        []string{"folder"},
	}
	got := liveRequired(act)
	if len(got) != 1 || got[0] != "folder" {
		t.Errorf("the model must be told the folder is required, got %v", got)
	}
}

// The api narrowing it exists for still works.
func TestApiActionStillNarrowsItsRequiredList(t *testing.T) {
	act := TempToolAction{
		Name:        "get",
		URLTemplate: "https://x/{id}",
		Params:      map[string]ToolParam{"id": {Type: "string"}, "verbose": {Type: "string"}},
		Required:    []string{"id", "verbose"},
	}
	got := liveRequired(act)
	if len(got) != 1 || got[0] != "id" {
		t.Errorf("only the path placeholder is really required, got %v", got)
	}
}

// A path-scoped parameter travels as a REACH, never as a read promise.
//
// The live failure: weka mapped exactly as its CLI reads — weka -l {logs} —
// was refused at dispatch because the scope on {logs} was taken for a promise
// that reads were confined to that folder. Nobody had promised that; the scope
// proves the model did not name ~/.ssh, and the command then has to be able to
// open what it was handed. The agent fell back to reading the same folder
// through the filestore tools, which have no sandbox in the way at all.
func TestScopedParamsTravelAsReachNotAPromise(t *testing.T) {
	src, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "res := sandbox.RunSandboxedShellIn(")
	if i < 0 {
		t.Fatal("the shell dispatch call has moved")
	}
	call := body[i:min(i+400, len(body))]
	if !strings.Contains(call, "Reach: scopedPaths") {
		t.Error("scoped paths must travel as Reach — as ReadOnly they are a promise " +
			"no Seatbelt host can keep, and every path-scoped tool is refused there")
	}
	if strings.Contains(call, "ReadOnly: scopedPaths") {
		t.Error("scoped paths must not claim read confinement")
	}
}
