package servitor

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A command that never ran must not be reported as a command that ran and said
// nothing. The old shape — "[exit code -1 — no output]" for every non-ExitError
// — cost a whole live session on a Mac: twenty commands, `echo hello` included,
// all identically empty while the session hunted for a binary that was there
// the whole time. The distinction these tests pin is the difference between
// "your command failed" and "nothing on this host is executing".
func TestExecLocalUnusableWorkDirSaysNothingRan(t *testing.T) {
	T := &Servitor{}
	out, err := T.exec_local_ctx(context.Background(), "echo hello", "/no/such/dir/anywhere", nil)
	if err != nil {
		t.Fatalf("unusable work dir should report in the output, not as a tool error: %v", err)
	}
	if !strings.Contains(out, "COMMAND DID NOT RUN") {
		t.Errorf("output does not say the command never ran: %q", out)
	}
	// The setting to change has to be named — the whole failure is invisible
	// otherwise, since it looks identical for every command against the system.
	if !strings.Contains(out, "Work Dir") {
		t.Errorf("output does not name the setting at fault: %q", out)
	}
	if strings.Contains(out, "exit code") {
		t.Errorf("output invents an exit status for a process that never started: %q", out)
	}
}

func TestExecLocalWorkDirThatIsAFile(t *testing.T) {
	// A file where a directory belongs fails the same way inside fork/exec, but
	// os.Stat succeeds on it, so it needs its own branch.
	f := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := (&Servitor{}).exec_local_ctx(context.Background(), "echo hello", f, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "COMMAND DID NOT RUN") || !strings.Contains(out, "not a directory") {
		t.Errorf("a file used as a working directory was not reported as such: %q", out)
	}
}

func TestExecLocalOrdinaryFailuresKeepTheirExitCode(t *testing.T) {
	T := &Servitor{}
	out, err := T.exec_local_ctx(context.Background(), "exit 3", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[exit code 3 — no output]") {
		t.Errorf("a real nonzero exit lost its status: %q", out)
	}
	if strings.Contains(out, "COMMAND DID NOT RUN") {
		t.Errorf("a command that ran was reported as never having run: %q", out)
	}
}

func TestExecLocalSignalKillIsNotConfusedWithNotRunning(t *testing.T) {
	// A signal kill has no exit status of its own, so ExitCode() is -1 — the
	// same number the old code invented for "never started".
	out, err := (&Servitor{}).exec_local_ctx(context.Background(), "kill -9 $$", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "signal") {
		t.Errorf("signal kill not named: %q", out)
	}
	if strings.Contains(out, "exit code -1") {
		t.Errorf("signal kill reported as exit code -1: %q", out)
	}
}

func TestExecLocalSucceeds(t *testing.T) {
	out, err := (&Servitor{}).exec_local_ctx(context.Background(), "echo hello", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "hello" {
		t.Errorf("got %q, want %q", out, "hello")
	}
}
