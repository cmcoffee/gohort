package sandbox

// A long-lived shell is confined by the same decision as a one-shot one.
//
// NewSandboxedShellCmd exists because tools/temptool's persistent shells need
// the *exec.Cmd rather than the bytes it prints. They used to build that
// command themselves — their own exec.LookPath("bwrap"), their own argv, and
// their own answer to "what if there is no bwrap", which was to log a warning
// and run the shell through the host's sh at the service account's full
// privilege. Every one-shot command on that same host was being refused for
// exactly that condition. The long-lived one walked past the gate because it
// had never been shown it.
//
// So the property under test is not "the builder works". It is that the
// builder REFUSES, and that the refusal cannot be mistaken for success by a
// caller that forgets to check the error.

import (
	"context"
	"strings"
	"testing"
)

// withBackend forces the resolved backend for one test. activeSandbox caches
// through a sync.Once, so the Once is burned first and the value swapped
// behind it; both are restored on cleanup.
func withBackend(t *testing.T, sb sandboxBackend) {
	t.Helper()
	sandboxOnce.Do(func() {})
	prev := sandboxActive
	sandboxActive = sb
	t.Cleanup(func() { sandboxActive = prev })
}

func TestALongLivedShellIsRefusedWhenTheHostCannotConfineIt(t *testing.T) {
	withBackend(t, noSandbox{})
	// Default policy. Named explicitly rather than relied upon: the whole
	// point is that an operator who set nothing gets the strict answer.
	t.Setenv("GOHORT_ALLOW_UNSANDBOXED", "")
	t.Setenv("GOHORT_SANDBOX_REQUIRED", "")

	built, err := NewSandboxedShellCmd(context.Background(), "psql -h db", t.TempDir(), nil)
	if err == nil {
		t.Fatal("a long-lived shell on a host with no sandbox must be refused, not opened unconfined")
	}
	// Nil rather than a usable command, so a caller that ignores the error
	// panics at Start instead of holding an unconfined shell for the rest of
	// the session. That is the direction this particular mistake must fall.
	if built.Cmd != nil {
		t.Error("a refused build must not hand back a runnable command")
	}
	if !strings.Contains(err.Error(), "no OS sandbox") {
		t.Errorf("the refusal should name the cause:\n%s", err)
	}
}

func TestALongLivedShellOpensUnconfinedOnlyWhenAskedTo(t *testing.T) {
	withBackend(t, noSandbox{})
	t.Setenv("GOHORT_ALLOW_UNSANDBOXED", "on")

	built, err := NewSandboxedShellCmd(context.Background(), "psql -h db", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("an explicit opt-out should permit the run: %v", err)
	}
	if built.Cmd == nil {
		t.Fatal("a permitted build must produce a command")
	}
	// Reported, not inferred. The caller logs this, and a log line that says
	// "unconfined" has to be naming the decision this package made rather
	// than a second guess at it.
	if built.Confined {
		t.Error("Confined must be false when the backend does not confine")
	}
	if built.Backend != "none" {
		t.Errorf("Backend should name the mechanism, got %q", built.Backend)
	}
}

// The admin bypass is a caller property, and an unstamped caller is not an
// admin. A persistent shell opened by a schedule or a channel wake has nobody
// at the keyboard, and must stay refused on a BypassAdmin host.
func TestALongLivedShellUnderTheAdminBypassStillRefusesAnUnstampedCaller(t *testing.T) {
	withBackend(t, noSandbox{})
	t.Setenv("GOHORT_ALLOW_UNSANDBOXED", "admin")

	if _, err := NewSandboxedShellCmd(context.Background(), "psql", t.TempDir(), nil); err == nil {
		t.Error("an unstamped caller is not an admin and must be refused")
	}
	if _, err := NewSandboxedShellCmd(WithAdminCaller(context.Background(), true), "psql", t.TempDir(), nil); err != nil {
		t.Errorf("an admin's own run should be permitted under the admin bypass: %v", err)
	}
}

// The built command carries the scrubbed environment, not the daemon's. A
// long-lived shell is the worst place to leak an API key: it sits in the
// LLM's reach for the rest of the session.
func TestALongLivedShellGetsTheScrubbedEnvironment(t *testing.T) {
	withBackend(t, noSandbox{})
	t.Setenv("GOHORT_ALLOW_UNSANDBOXED", "on")
	t.Setenv("SOME_PROVIDER_API_KEY", "sk-do-not-leak-this")

	built, err := NewSandboxedShellCmd(context.Background(), "env", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, kv := range built.Cmd.Env {
		if strings.HasPrefix(kv, "SOME_PROVIDER_API_KEY=") {
			t.Fatal("the daemon's secrets must not reach a long-lived shell's environment")
		}
	}
}
