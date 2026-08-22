package sandbox

// Binding the broker's socket into the sandbox.
//
// The broker itself lives in core — what a confined process may ask for is
// credential enforcement, not confinement. What is HERE is the mechanics half:
// given a socket path, does the sandbox make it reachable, exactly once, before
// the separator bwrap reads the command after.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookSocketDirName mirrors the broker's short-path directory. Duplicated
// rather than imported: a leaf cannot import the package it left, and these
// tests only need a plausible path shape to reason about the bind.
const hookSocketDirName = "gohort-hooks"

func TestASocketOutsideTheWorkspaceGetsItsOwnBind(t *testing.T) {
	ws := "/tmp/ws"
	sock := filepath.Join(os.TempDir(), hookSocketDirName, "deadbeef.sock")
	argv := bwrapArgvWithEnv(ws, "true", map[string]string{"GOHORT_HOOK_PATH": sock}, false)

	var bound bool
	for i := 0; i+2 < len(argv); i++ {
		if argv[i] == "--bind" && argv[i+1] == sock && argv[i+2] == sock {
			bound = true
		}
	}
	if !bound {
		t.Errorf("a socket outside the workspace needs its own bind, got:\n%v", argv)
	}
	// The bind must land before the "--" separator or bwrap reads it as part
	// of the command to exec.
	sep, at := -1, -1
	for i, a := range argv {
		if a == "--" && sep < 0 {
			sep = i
		}
		if a == "--bind" && i+1 < len(argv) && argv[i+1] == sock {
			at = i
		}
	}
	if at > sep {
		t.Errorf("the bind landed after the separator (%d > %d)", at, sep)
	}
}

func TestASocketInsideTheWorkspaceIsNotBoundTwice(t *testing.T) {
	// The workspace is already bind-mounted whole. Mounting a file inside it
	// again is a second mount over an existing one, which bwrap rejects —
	// taking down every hook-using tool on a deployment whose paths were
	// short enough that nothing was ever broken.
	ws := "/tmp/ws"
	sock := filepath.Join(ws, ".gohort_hook_deadbeef.sock")
	argv := bwrapArgvWithEnv(ws, "true", map[string]string{"GOHORT_HOOK_PATH": sock}, false)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--bind" && argv[i+1] == sock {
			t.Errorf("a socket already inside the workspace was bound again:\n%v", argv)
		}
	}
	// It is still handed to the script either way.
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "GOHORT_HOOK_PATH") {
		t.Errorf("the path must still reach the sandbox env:\n%v", argv)
	}
}

func TestWithinDirIsNotAPrefixMatch(t *testing.T) {
	if !withinDir("/tmp/ws/a.sock", "/tmp/ws") {
		t.Error("a file in the dir is within it")
	}
	// "/tmp/ws-other" starts with "/tmp/ws" as a string and is a different
	// directory. A prefix test here would skip the bind and leave the socket
	// unreachable inside the sandbox.
	if withinDir("/tmp/ws-other/a.sock", "/tmp/ws") {
		t.Error("a sibling with a shared prefix is not within")
	}
	if withinDir("/other/a.sock", "/tmp/ws") {
		t.Error("an unrelated path is not within")
	}
	// No workspace means nothing contains it, so the bind is added.
	if withinDir("/tmp/a.sock", "") {
		t.Error("an empty dir contains nothing")
	}
}
