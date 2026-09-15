package temptool

// A mapped command that must run INSIDE its folder. The folder is proved by a
// path scope, then carried as a working directory rather than as a scoped read
// — the distinction that keeps it working on a backend whose sandbox cannot
// scope a read.

import (
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

// Not supplied on this call: the command still runs, in the workspace, exactly
// as it did before the field existed.
func TestWorkDirOmittedOnACallIsNotAnError(t *testing.T) {
	tt := scopedFolderTool()
	dir, scoped, err := splitWorkDir(tt, map[string]any{}, []string{"/srv/x"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if dir != "" {
		t.Errorf("want no cwd, got %q", dir)
	}
	if len(scoped) != 1 {
		t.Errorf("other scoped paths should be untouched: %v", scoped)
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
