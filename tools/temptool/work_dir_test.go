package temptool

// A mapped command that must run INSIDE its folder. The folder is proved by a
// path scope, then carried as a working directory rather than as a scoped read
// — the distinction that keeps it working on a backend whose sandbox cannot
// scope a read.

import (
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

// A refused path-scoped run has to name the FIELD, not just the path.
//
// The sandbox's own refusal named "/Users/.../DIAG_DUMPS/kiteworks-au-h1" and
// stopped, because a path is all that layer has. The reader's questions —
// which of my parameters is that, and what do I change — are answerable only
// here, and for a command that merely needs to run inside the folder the
// answer is one field.
func TestScopedReadRefusalNamesTheParameterAndTheFix(t *testing.T) {
	tt := &TempTool{
		Name: "weka", Mode: TempToolModeShell,
		CommandTemplate: "/opt/bin/weka syshealth {folder}",
		Params: map[string]ToolParam{
			"folder":  {Type: "string", PathScope: "files:diag-dumps"},
			"verbose": {Type: "string"},
		},
	}
	args := map[string]any{
		"folder":  "/Users/x/Downloads/DIAG_DUMPS/kiteworks-au-h1",
		"verbose": "1",
	}
	err := scopedReadRefusal(tt, args, []string{"/Users/x/Downloads/DIAG_DUMPS/kiteworks-au-h1"})
	msg := err.Error()

	if !strings.Contains(msg, `"folder"`) {
		t.Errorf("the refusal must name the parameter carrying the path: %s", msg)
	}
	if !strings.Contains(msg, "files:diag-dumps") {
		t.Errorf("it should say which scope the parameter declares: %s", msg)
	}
	if !strings.Contains(msg, "work_dir") {
		t.Errorf("it must name the field that resolves this: %s", msg)
	}
	if !strings.Contains(msg, "Nothing ran") && !strings.Contains(msg, "nothing ran") {
		t.Errorf("it must be clear the command did not run: %s", msg)
	}
	// A parameter with no scope is not implicated.
	if strings.Contains(msg, "verbose") {
		t.Errorf("an unscoped parameter should not be named: %s", msg)
	}
}

// With nothing identifiable it still says what to change rather than nothing.
func TestScopedReadRefusalWithoutAKnownCarrier(t *testing.T) {
	tt := &TempTool{Name: "x", Mode: TempToolModeShell, CommandTemplate: "true"}
	msg := scopedReadRefusal(tt, map[string]any{}, []string{"/srv/corpus"}).Error()
	if !strings.Contains(msg, "scoped parameter") || !strings.Contains(msg, "work_dir") {
		t.Errorf("it should still be actionable: %s", msg)
	}
}
