package temptool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Which entry point a network call comes from decides whether the workspace's
// reach ceiling applies to it.

// A granted tool may reach the network through the broker even when the
// agent's workspace may not dial out. A draft the agent wrote this turn may
// not, or tool_def is the way around the ceiling.
func TestOnlyAToolInThePoolEarnsTheReachLift(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	sess := &ToolSession{DB: db, Username: "alice"}
	if err := AdminPersistTempTool(db, "alice", TempTool{
		Name: "granted_fetcher", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if !toolIsGranted(sess, &TempTool{Name: "granted_fetcher"}) {
		t.Error("a tool in the owner's pool was not recognized as granted")
	}
	if toolIsGranted(sess, &TempTool{Name: "just_minted"}) {
		t.Error("a draft the agent authored this turn earned the lift, so tool_def routes around the ceiling")
	}
	// Unresolvable reads as NOT granted. A privilege handed out when the
	// answer could not be determined is a hole, not a privilege.
	if toolIsGranted(nil, &TempTool{Name: "granted_fetcher"}) {
		t.Error("no session read as granted")
	}
	if toolIsGranted(sess, &TempTool{Name: "  "}) {
		t.Error("a blank name read as granted")
	}
}

// The three shapes a shell tool dispatches in (dispatchTempToolUncached): a
// recipe deployed into a fresh folder per run, with its state carried from
// one run to the next; a plain command given a throwaway folder; and a tool
// whose script is missing from the folder it names. Each folder made for a
// run is gone when the run ends.

// dispatchRoot is a workspaces folder outside /tmp: the sandbox gives the
// command a fresh /tmp, so a run folder under it could not be entered.
func dispatchRoot(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for a workspaces folder outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-dispatch-test-")
	if err != nil {
		t.Skip("cannot make a workspaces folder outside /tmp: ", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	SetWorkspacesDir(root)
	t.Cleanup(func() { SetWorkspacesDir("") })
	return root
}

// leftOver is what a run left in the folder runs are minted under.
func leftOver(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(root, "_dispatch"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestARecipeToolCarriesItsStateFromRunToRun(t *testing.T) {
	root := dispatchRoot(t)
	sess := &ToolSession{Username: "alice"}
	counter := &TempTool{Name: "counter", Mode: TempToolModeShell, StatePath: "state",
		Recipe:          []RecipeFile{{Path: "count.sh", Content: "n=$(cat state/n 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > state/n\necho run $n\n", Mode: 0o700}},
		CommandTemplate: "cd {workspace_dir} && sh count.sh"}
	for want := 1; want <= 2; want++ {
		out, err := dispatchTempToolUncached(sess, counter, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out) != "run "+string(rune('0'+want)) {
			t.Errorf("run %d printed %q", want, out)
		}
		if left := leftOver(t, root); len(left) != 0 {
			t.Errorf("run %d left its folder behind: %v", want, left)
		}
	}
}

func TestAPlainCommandRunsInAFolderThatGoes(t *testing.T) {
	root := dispatchRoot(t)
	out, err := dispatchTempToolUncached(&ToolSession{Username: "alice"},
		&TempTool{Name: "hello", Mode: TempToolModeShell, CommandTemplate: "echo hello"}, map[string]any{})
	if err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("%q %v", out, err)
	}
	if left := leftOver(t, root); len(left) != 0 {
		t.Errorf("the throwaway folder was left behind: %v", left)
	}
	// One that names the working folder has nowhere to run without one.
	if _, err := dispatchTempToolUncached(&ToolSession{Username: "alice"},
		&TempTool{Name: "needs_ws", Mode: TempToolModeShell, CommandTemplate: "ls {workspace_dir}"}, map[string]any{}); err == nil || !strings.Contains(err.Error(), "has no sandbox") {
		t.Errorf("a tool naming {workspace_dir} with no working folder should be refused: %v", err)
	}
}

func TestAToolWhoseScriptIsGoneIsRefused(t *testing.T) {
	root := dispatchRoot(t)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := dispatchTempToolUncached(&ToolSession{Username: "alice", WorkspaceDir: ws},
		&TempTool{Name: "gone", Mode: TempToolModeShell, CommandTemplate: "python3 {workspace_dir}/gone.py"}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "aren't on disk") {
		t.Errorf("a script that is not on disk should be refused with the fix: %v", err)
	}
}

// The run's cleanups behave as the defers they replace: last added runs
// first, and one that panics does not stop the rest.
func TestTheExitListRunsLikeDefers(t *testing.T) {
	var ran []int
	func() {
		defer func() { _ = recover() }()
		var l exitList
		defer l.run()
		l.add(func() { ran = append(ran, 1) })
		l.add(func() { panic("boom") })
		l.add(func() { ran = append(ran, 3) })
	}()
	if len(ran) != 2 || ran[0] != 3 || ran[1] != 1 {
		t.Errorf("cleanups ran %v, want [3 1]", ran)
	}
}

// A run that cannot be put together stops before it runs, with the reason:
// a parameter the command names and the call left out, and a parameter
// naming a file that is not in the working folder.
func TestARunThatCannotBeAssembledStopsWithTheReason(t *testing.T) {
	root := dispatchRoot(t)
	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	greet := &TempTool{Name: "greet", Mode: TempToolModeShell, CommandTemplate: "echo {name}",
		Params: map[string]ToolParam{"name": {Type: "string"}}}
	if out, err := dispatchTempToolUncached(sess, greet, map[string]any{}); err == nil || !strings.Contains(err.Error(), `missing arg "name"`) {
		t.Errorf("a parameter the command names and the call left out should stop it: %q %v", out, err)
	}
	if out, err := dispatchTempToolUncached(sess, greet, map[string]any{"name": map[string]any{"file": "absent.txt"}}); err == nil || !strings.Contains(err.Error(), `names file "absent.txt"`) {
		t.Errorf("a file that is not there should stop it: %q %v", out, err)
	}
}

// End-to-end: a TOOLBOX action carrying work_dir must actually START in the
// resolved folder. splitWorkDir is unit-tested next door; this asks the
// question the unit test cannot, which is whether the value survives the
// toolbox → synthetic-shell-tool → sandbox path and reaches the process.

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
