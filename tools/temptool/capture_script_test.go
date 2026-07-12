package temptool

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCaptureOnDiskScriptIntoRecord verifies that a shell tool authored via
// local(write) + a {workspace_dir} command_template reference (i.e. WITHOUT the
// script_body param) still captures the on-disk script into the tool record —
// so it travels with an export bundle and survives a workspace wipe. This is
// the fix for "exporting tools doesn't include the python script."
func TestCaptureOnDiskScriptIntoRecord(t *testing.T) {
	ws := t.TempDir()
	const body = "import os\nprint('hello', os.environ.get('who'))\n"
	if err := os.WriteFile(filepath.Join(ws, "hello.py"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}

	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "sess1",
		WorkspaceDir:  ws,
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	args := map[string]any{
		"name":             "greet",
		"description":      "greets",
		"command_template": "python3 {workspace_dir}/hello.py",
		"params":           map[string]any{"who": map[string]any{"type": "string"}},
	}
	if _, err := (&CreateTempToolTool{}).RunWithSession(args, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	tools := LoadSessionTempTools(sess.DB, sess.ChatSessionID)
	var got *TempTool
	for i := range tools {
		if tools[i].Name == "greet" {
			got = &tools[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("tool not saved; have %d session tools", len(tools))
	}
	if got.ScriptBody != body {
		t.Errorf("ScriptBody not captured.\n got: %q\nwant: %q", got.ScriptBody, body)
	}
	if got.ScriptName != "hello.py" {
		t.Errorf("ScriptName = %q, want hello.py", got.ScriptName)
	}
	if got.CanonicalScriptName == "" {
		t.Errorf("CanonicalScriptName empty — dispatch redeploy would have no on-disk target")
	}
}

// TestCaptureExportScriptBackfill verifies the export-time backfill for LEGACY
// tools: a persisted tool whose script lives only on disk (empty ScriptBody)
// gets its body read back from the owner's workspace at export time, so the
// script travels with the bundle.
func TestCaptureExportScriptBackfill(t *testing.T) {
	root := t.TempDir()
	SetWorkspacesDir(root)
	owner := "bob"
	dir := filepath.Join(root, owner)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const body = "#!/bin/bash\necho hi\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}

	// A legacy record: references the on-disk script, but ScriptBody is empty.
	tool := &TempTool{Name: "runner", CommandTemplate: "bash {workspace_dir}/run.sh"}
	if ResolveToolScriptForExport == nil {
		t.Fatal("ResolveToolScriptForExport not wired by init()")
	}
	ResolveToolScriptForExport(tool, owner)

	if tool.ScriptBody != body {
		t.Errorf("backfill missed: ScriptBody=%q want %q", tool.ScriptBody, body)
	}
	if tool.ScriptName != "run.sh" {
		t.Errorf("ScriptName=%q want run.sh", tool.ScriptName)
	}
}

// TestCaptureMultiFileTool verifies that a shell tool whose entry script
// imports a helper module captures BOTH into the record: the entry as
// ScriptBody and the helper (found transitively on disk) as a WorkspaceFile,
// so the whole tool travels with an export.
func TestCaptureMultiFileTool(t *testing.T) {
	ws := t.TempDir()
	const mainBody = "import helper\nprint(helper.hi())\n"
	const helperBody = "def hi():\n    return 'hi'\n"
	if err := os.WriteFile(filepath.Join(ws, "main.py"), []byte(mainBody), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "helper.py"), []byte(helperBody), 0700); err != nil {
		t.Fatal(err)
	}

	sess := &ToolSession{
		Username:      "carol",
		ChatSessionID: "s1",
		WorkspaceDir:  ws,
		DB:            &DBase{Store: kvlite.MemStore()},
	}
	args := map[string]any{
		"name":             "greeter",
		"description":      "greets",
		"command_template": "python3 {workspace_dir}/main.py",
		"params":           map[string]any{"who": map[string]any{"type": "string"}},
	}
	if _, err := (&CreateTempToolTool{}).RunWithSession(args, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	var got *TempTool
	for _, tt := range LoadSessionTempTools(sess.DB, "s1") {
		if tt.Name == "greeter" {
			g := tt
			got = &g
			break
		}
	}
	if got == nil {
		t.Fatal("tool not saved")
	}
	if got.ScriptBody != mainBody {
		t.Errorf("entry ScriptBody = %q, want %q", got.ScriptBody, mainBody)
	}
	if len(got.WorkspaceFiles) != 1 || got.WorkspaceFiles[0].Path != "helper.py" {
		t.Fatalf("WorkspaceFiles = %+v, want [helper.py]", got.WorkspaceFiles)
	}
	if got.WorkspaceFiles[0].Content != helperBody {
		t.Errorf("helper content = %q, want %q", got.WorkspaceFiles[0].Content, helperBody)
	}
}

func TestGatherWorkspaceHelpersTransitiveAndSafe(t *testing.T) {
	ws := t.TempDir()
	// a imports b (transitive); b imports os (stdlib — not on disk, skipped);
	// c.py exists but nobody imports it (not captured). main.py is the entry
	// and must not appear in its own helper set.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("main.py", "import a\n")
	write("a.py", "import b, os\n")
	write("b.py", "print('leaf')\n")
	write("c.py", "print('orphan')\n")

	helpers := gatherWorkspaceHelpers("main.py", "import a\n", ws)
	got := map[string]bool{}
	for _, h := range helpers {
		got[h.Path] = true
	}
	if !got["a.py"] || !got["b.py"] {
		t.Errorf("expected a.py and b.py captured, got %v", got)
	}
	if got["c.py"] {
		t.Error("c.py should NOT be captured (unreferenced)")
	}
	if got["main.py"] {
		t.Error("entry main.py should be excluded from its own helper set")
	}
	if got["os.py"] {
		t.Error("stdlib os should not resolve to a file")
	}
}

func TestScriptHelperRefs(t *testing.T) {
	py := "from foo import x\nimport bar, baz\nfrom .rel import y\n"
	got := scriptHelperRefs(py, ".py")
	want := map[string]bool{"foo.py": true, "bar.py": true, "baz.py": true, "rel.py": true}
	if len(got) != len(want) {
		t.Fatalf("py refs = %v, want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected py ref %q", g)
		}
	}
	sh := "source lib.sh\n. ./util.sh\n"
	shGot := scriptHelperRefs(sh, ".sh")
	if len(shGot) != 2 {
		t.Fatalf("sh refs = %v, want [lib.sh util.sh]", shGot)
	}
}

func TestPresentWorkspaceScriptRefs(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.py"), []byte("x"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := "python3 {workspace_dir}/a.py && cat {workspace_dir}/out.json && python3 {workspace_dir}/missing.py"
	present := presentWorkspaceScriptRefs(cmd, ws)
	// a.py exists and is a script; out.json is not a script ext; missing.py
	// doesn't exist. Only a.py should surface.
	if len(present) != 1 || present[0] != "a.py" {
		t.Fatalf("present = %v, want [a.py]", present)
	}
}
