package sandbox

// The third rule: refuse rather than degrade.
//
// Everywhere else in sandbox_exec an absent sandbox is a warning and the
// command still runs, because the alternative is a deployment where no
// tool works at all. Here the caller has been promised something
// specific — this path, read-only, nothing else — and without a mount
// namespace that promise is not kept: the command would run with the
// daemon's own view of the filesystem, where the resolved path is
// readable and so is everything around it.
//
// Driven against the backends directly rather than through a run, so it
// exercises the unconfined case on a host that HAS bubblewrap. The
// earlier version of this test skipped here, and a skipped test proves
// nothing about the case it names.

import (
	"strings"
	"testing"
)

func TestAScopedRunRefusesWhenNothingCanConfineIt(t *testing.T) {
	scoped := []string{"/var/log/bundles/a"}

	err := scopedRunRefusal(noSandbox{}, scoped)
	if err == nil {
		t.Fatal("a scoped run with no confinement must refuse, not run unconstrained")
	}
	msg := err.Error()
	// The message is read by an operator whose tool just stopped working
	// for a reason that is not in their tool, so it has to name the path,
	// the missing thing, and both ways out. The install advice is asserted
	// through unsandboxedAdvice() rather than as a literal, because it is
	// platform-derived: hardcoding "bubblewrap" here would be the same bug
	// this whole file family exists to prevent, one level up.
	for _, want := range []string{"/var/log/bundles/a", "no sandbox", "none", unsandboxedAdvice(), "path_scope"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, msg)
		}
	}

	// Nothing promised: no refusal, even unconfined. The refusal is about
	// the promise, not about the sandbox being absent — otherwise every
	// ordinary shell tool would stop working on a host without bwrap.
	if err := scopedRunRefusal(noSandbox{}, nil); err != nil {
		t.Errorf("an unscoped run must still be allowed: %v", err)
	}

	// A backend that scopes reads keeps the promise, so it runs.
	if err := scopedRunRefusal(bwrapSandbox{path: "/usr/bin/bwrap"}, scoped); err != nil {
		t.Errorf("a read-scoping backend should be allowed to run it: %v", err)
	}
}

// TestAConfiningBackendThatCannotScopeAReadStillRefuses is the case that was
// live and wrong: Seatbelt confines, so the guard's old confines() test passed,
// and the command then ran with the scoped folder readable AND the rest of the
// filesystem with it — a path_scope that had been checked and then applied to
// nothing.
//
// Runs on Linux against the darwin backend deliberately. The behavior belongs
// to the backend, not to the host, and the machine this suite runs on is not
// the machine that was affected.
func TestAConfiningBackendThatCannotScopeAReadStillRefuses(t *testing.T) {
	sb := seatbeltSandbox{path: seatbeltBinary}
	scoped := []string{"/var/log/bundles/a"}

	if !sb.confines() {
		t.Fatal("seatbelt must still report that it confines — writes and network are governed")
	}
	if sb.scopesReads() {
		t.Fatal("seatbelt cannot scope a read: the profile allows file-read* filesystem-wide")
	}

	err := scopedRunRefusal(sb, scoped)
	if err == nil {
		t.Fatal("a scoped run under a backend that cannot narrow reads must refuse, not run unscoped")
	}
	msg := err.Error()
	// Names the path, the backend, why the scope buys nothing here, and the
	// two real moves. Explicitly NOT install advice: there is nothing to
	// install on macOS that would change this answer.
	for _, want := range []string{"/var/log/bundles/a", "seatbelt", "filesystem-wide", "bubblewrap", "path_scope"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "no sandbox") {
		t.Errorf("this host HAS a sandbox — the message must not read as an absent one:\n%s", msg)
	}

	// Unscoped runs are untouched: every ordinary shell tool on a Mac still
	// works, which is the difference between this refusal and a regression.
	if err := scopedRunRefusal(sb, nil); err != nil {
		t.Errorf("an unscoped run under seatbelt must still be allowed: %v", err)
	}
}
