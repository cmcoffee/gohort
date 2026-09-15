package sandbox

import (
	"strings"
	"testing"
)

// cwd() is the one place four backends agree on where a command starts.
func TestCwdFallsBackToTheWorkspace(t *testing.T) {
	if got := (sandboxRun{WorkspaceDir: "/ws"}).cwd(); got != "/ws" {
		t.Errorf("an unset WorkDir should mean the workspace, got %q", got)
	}
	if got := (sandboxRun{WorkspaceDir: "/ws", WorkDir: "  "}).cwd(); got != "/ws" {
		t.Errorf("blank is unset, got %q", got)
	}
	if got := (sandboxRun{WorkspaceDir: "/ws", WorkDir: "/dumps"}).cwd(); got != "/dumps" {
		t.Errorf("WorkDir should win when set, got %q", got)
	}
}

// Under bubblewrap the bind is not a permission the cwd needs, it is the
// reason the cwd resolves: --chdir at an unbound path exits with "Can't
// chdir" before the command runs.
func TestWorkDirOutsideTheWorkspaceIsBoundAndChdirred(t *testing.T) {
	args := []string{"--bind", "/ws", "/ws", "--chdir", "/ws", "--", "sh", "-c", "weka syshealth"}
	got := withWorkDir(args, "/dumps/DIAG", "/ws")

	sep := strings.Index(strings.Join(got, "\x00"), "\x00--\x00")
	if sep < 0 {
		t.Fatal("the separator went missing")
	}
	flags := strings.Join(got, " ")
	if !strings.Contains(flags, "--ro-bind-try /dumps/DIAG /dumps/DIAG") {
		t.Errorf("an outside WorkDir needs a bind to exist at all: %v", got)
	}
	// Rewritten, not appended: bwrap takes the last --chdir, so a duplicate
	// would work by accident today and break when the argv order changed.
	if n := strings.Count(flags, "--chdir"); n != 1 {
		t.Errorf("want exactly one --chdir, got %d: %v", n, got)
	}
	if !strings.Contains(flags, "--chdir /dumps/DIAG") {
		t.Errorf("--chdir should point at the WorkDir: %v", got)
	}
	if strings.Contains(flags, "--chdir /ws") {
		t.Error("the workspace --chdir should have been replaced, not kept")
	}
	// The bind is READ-ONLY. WorkDir says where to start, never what may be
	// written; the workspace stays the only writable path.
	if strings.Contains(flags, "--bind /dumps") {
		t.Error("WorkDir must not be writable")
	}
	if !strings.HasSuffix(flags, "-- sh -c weka syshealth") {
		t.Errorf("the command was disturbed: %v", got)
	}
}

// A WorkDir already inside the workspace is bound by the workspace bind; a
// second mount over it is what bwrap rejects.
func TestWorkDirInsideTheWorkspaceIsNotBoundTwice(t *testing.T) {
	args := []string{"--bind", "/ws", "/ws", "--chdir", "/ws", "--", "sh", "-c", "ls"}
	got := withWorkDir(args, "/ws/sub", "/ws")

	flags := strings.Join(got, " ")
	if strings.Contains(flags, "--ro-bind-try /ws/sub") {
		t.Errorf("a path under the workspace is already there: %v", got)
	}
	if !strings.Contains(flags, "--chdir /ws/sub") {
		t.Errorf("it should still be the cwd: %v", got)
	}
}

// Nothing set, nothing changed — the argv every existing caller gets today.
func TestNoWorkDirLeavesTheArgvAlone(t *testing.T) {
	args := []string{"--bind", "/ws", "/ws", "--chdir", "/ws", "--", "sh", "-c", "ls"}
	before := strings.Join(args, " ")
	if got := strings.Join(withWorkDir(args, "", "/ws"), " "); got != before {
		t.Errorf("an unset WorkDir should change nothing:\n got %s\nwant %s", got, before)
	}
}

// The none backend confines nothing, but it still has to start in the right
// place: it is the macOS-without-seatbelt and the bwrap-less Linux path.
func TestNoneBackendStartsInTheWorkDir(t *testing.T) {
	c := noSandbox{}.build(t.Context(), sandboxRun{
		Kind: sandboxShellRun, Command: "pwd", WorkspaceDir: "/ws", WorkDir: "/dumps/DIAG",
	})
	if c.Dir != "/dumps/DIAG" {
		t.Errorf("want the WorkDir as cwd, got %q", c.Dir)
	}
}

// WorkDir must not be smuggled through ReadOnly: that field is a promise
// reads are confined to it, and scopedRunRefusal refuses the whole run on
// any backend that cannot keep it — Seatbelt included, which is the one
// platform this feature came from.
func TestWorkDirDoesNotTripTheScopedReadRefusal(t *testing.T) {
	if err := scopedRunRefusal(seatbeltSandbox{}, nil); err != nil {
		t.Errorf("a run with no ReadOnly should pass on seatbelt, got %v", err)
	}
}
