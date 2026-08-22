package sandbox

import (
	"strings"
	"testing"
)

// A proved path the script cannot open is a check that passes and a tool
// that fails, which reads as the check being wrong. The bind is what
// closes that.
func TestReadOnlyBindsLandBeforeTheCommand(t *testing.T) {
	args := []string{"--bind", "/ws", "/ws", "--", "sh", "-c", "cat /logs/x"}
	got := withReadOnlyBinds(args, []string{"/logs/bundle-a"}, "/ws")

	sep := -1
	for i, a := range got {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatal("the separator went missing")
	}
	flags := strings.Join(got[:sep], " ")
	if !strings.Contains(flags, "--ro-bind-try /logs/bundle-a /logs/bundle-a") {
		t.Errorf("the bind should be a flag, same path both sides: %v", got)
	}
	// --ro-bind-try, not --ro-bind: a path removed since it resolved
	// should leave the script reporting "no such file" rather than bwrap
	// refusing to start, which is an error a model can act on versus one
	// it cannot see past.
	if strings.Contains(flags, " --ro-bind /logs") {
		t.Error("a vanished path should not stop the sandbox from starting")
	}
	// The command survives intact.
	if strings.Join(got[sep:], " ") != "-- sh -c cat /logs/x" {
		t.Errorf("the command was disturbed: %v", got[sep:])
	}

	// A path already inside the workspace is skipped: it is bound
	// WRITABLE there, and binding it read-only afterwards would take the
	// write away.
	inside := withReadOnlyBinds(args, []string{"/ws/sub"}, "/ws")
	if strings.Contains(strings.Join(inside, " "), "/ws/sub") {
		t.Errorf("a workspace path must not be re-bound read-only: %v", inside)
	}
	// Duplicates collapse, and nothing is added for an empty list.
	twice := withReadOnlyBinds(args, []string{"/logs/a", "/logs/a", "  "}, "/ws")
	if n := strings.Count(strings.Join(twice, " "), "--ro-bind-try"); n != 1 {
		t.Errorf("expected one bind, got %d: %v", n, twice)
	}
	if len(withReadOnlyBinds(args, nil, "/ws")) != len(args) {
		t.Error("no scoped paths should mean no change at all")
	}
}
