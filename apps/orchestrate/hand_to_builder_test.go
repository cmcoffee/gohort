package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// withWorkspace points EnsureWorkspaceDir at a temp dir for the test user.
func withWorkspace(t *testing.T) (user, dir string) {
	t.Helper()
	user = "handoff@example.com"
	SetWorkspacesDir(t.TempDir())
	d, err := EnsureWorkspaceDir(user)
	if err != nil {
		t.Fatalf("workspace setup failed: %v", err)
	}
	return user, d
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The point of the tool is handing over the code that ACTUALLY RAN. A file the
// agent names but that isn't there must be reported, never silently dropped —
// otherwise Builder builds from the brief alone, which is the failure this
// exists to stop.
func TestCollectHandoffFilesReportsMissing(t *testing.T) {
	user, dir := withWorkspace(t)
	write(t, dir, "good.py", "print('hi')")
	turn := &chatTurn{user: user}

	files, warn, err := turn.collectHandoffFiles([]string{"good.py", "gone.py"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != 1 || files[0].name != "good.py" {
		t.Fatalf("wanted the one real file, got %+v", files)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "gone.py") {
		t.Errorf("missing file not reported: %v", warn)
	}
}

// A path could walk out of the workspace and hand Builder a file the agent was
// never meant to reach.
func TestCollectHandoffFilesRefusesPaths(t *testing.T) {
	user, dir := withWorkspace(t)
	write(t, dir, "ok.py", "x = 1")
	turn := &chatTurn{user: user}

	for _, bad := range []string{"../../etc/passwd", "sub/dir.py", `..\win.ini`, ".", ".."} {
		files, warn, err := turn.collectHandoffFiles([]string{bad})
		if err != nil {
			t.Fatalf("collect(%q): %v", bad, err)
		}
		if len(files) != 0 {
			t.Errorf("%q was accepted — it must be refused", bad)
		}
		if len(warn) == 0 {
			t.Errorf("%q refused silently; the agent must be told", bad)
		}
	}
}

// Big files are previewed, not inlined whole — a large script would otherwise
// crowd out the brief that explains it. The preview must say it's truncated so
// Builder knows to read the file.
func TestCollectHandoffFilesTruncatesPreview(t *testing.T) {
	user, dir := withWorkspace(t)
	write(t, dir, "big.py", strings.Repeat("a", handoffPreviewBytes*3))
	turn := &chatTurn{user: user}

	files, _, err := turn.collectHandoffFiles([]string{"big.py"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	if !files[0].truncated {
		t.Error("large file not marked truncated")
	}
	if len(files[0].preview) > handoffPreviewBytes {
		t.Errorf("preview is %d bytes, over the %d cap", len(files[0].preview), handoffPreviewBytes)
	}
	// The real size must survive so Builder can see how much it's missing.
	if files[0].size != int64(handoffPreviewBytes*3) {
		t.Errorf("size = %d, want the full file size", files[0].size)
	}
}

// Empty files and duplicates are noise; the cap keeps one handoff bounded.
func TestCollectHandoffFilesNormalizes(t *testing.T) {
	user, dir := withWorkspace(t)
	write(t, dir, "a.py", "x")
	write(t, dir, "empty.py", "")
	turn := &chatTurn{user: user}

	files, warn, _ := turn.collectHandoffFiles([]string{"a.py", "a.py", "  a.py  ", "empty.py"})
	if len(files) != 1 {
		t.Errorf("duplicates not collapsed: %+v", files)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "empty") {
		t.Errorf("empty file not reported: %v", warn)
	}

	var many []string
	for i := 0; i < maxHandoffFiles+5; i++ {
		n := string(rune('a'+i)) + ".py"
		write(t, dir, n, "body")
		many = append(many, n)
	}
	got, warns, _ := turn.collectHandoffFiles(many)
	if len(got) > maxHandoffFiles {
		t.Errorf("handed %d files, over the %d cap", len(got), maxHandoffFiles)
	}
	if len(warns) == 0 {
		t.Error("files dropped by the cap must be reported, not silently lost")
	}
}

func TestCollectHandoffFilesEmptyInput(t *testing.T) {
	user, _ := withWorkspace(t)
	turn := &chatTurn{user: user}
	files, warn, err := turn.collectHandoffFiles(nil)
	if err != nil || len(files) != 0 || len(warn) != 0 {
		t.Errorf("empty input should be a clean no-op: %v %v %v", files, warn, err)
	}
}

// The tool must not appear for an agent that cannot dispatch Builder — a tool
// whose entire purpose is dispatching Builder would just fail on use.
func TestHandToBuilderHiddenWithoutDispatchRights(t *testing.T) {
	plain := &chatTurn{agent: AgentRecord{ID: "plain"}}
	if handToBuilderTool(plain) != nil {
		t.Error("offered to an agent with no Builder dispatch rights")
	}
	if handToBuilderTool(nil) != nil {
		t.Error("nil turn should yield no tool")
	}

	fleet := &chatTurn{agent: AgentRecord{ID: "fleeter", Fleet: true}}
	if handToBuilderTool(fleet) == nil {
		t.Error("a Fleet agent may dispatch Builder, so it should get the tool")
	}
	granted := &chatTurn{agent: AgentRecord{ID: "g", AllowBuilderDispatch: true}}
	if handToBuilderTool(granted) == nil {
		t.Error("an agent granted Builder dispatch should get the tool")
	}
}
