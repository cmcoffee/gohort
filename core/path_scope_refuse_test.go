package core

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
	// the missing thing, and both ways out.
	for _, want := range []string{"/var/log/bundles/a", "no sandbox", "none", "bubblewrap", "path_scope"} {
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

	// A backend that confines keeps the promise, so it runs.
	if err := scopedRunRefusal(bwrapSandbox{path: "/usr/bin/bwrap"}, scoped); err != nil {
		t.Errorf("a confining backend should be allowed to run it: %v", err)
	}
}
