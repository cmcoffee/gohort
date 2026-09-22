package core

// The workspace's reach ceiling has to distinguish WHERE a network call is
// coming from, and for a while it could not. One turn dispatches a granted
// tool and an ad-hoc workspace command through the same ToolSession, so a
// check read off the session answers the same for both.
//
// It answered wrong in both directions: a granted tool reaching out through
// the brokered fetch hook was refused, and workspace(action=run) got out
// anyway because its sandbox context was rooted at Background and never saw
// the ceiling at all.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

// The decision is a property of the RUN. Reading it off the session is what
// made the two entry points indistinguishable.
func TestTheReachCeilingIsAskedOfTheRunNotTheSession(t *testing.T) {
	src, err := os.ReadFile("sandbox_hook.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "!h.WorkspaceNetExempt && !h.Sess.WorkspaceNetworkAllowed()") {
		t.Error("the fetch hook decides from the session alone, so a granted tool and an ad-hoc command read the same")
	}
}

// Every sandboxed exec must inherit the turn's context. Both ceilings ride it
// and both default to ALLOWED when absent, so a Background root does not fail
// safe, it fails open.
func TestNoSandboxedExecRootsItsContextAtBackground(t *testing.T) {
	for _, f := range []string{
		"../tools/workspace/workspace.go",
		"../tools/temptool/dispatch.go",
		"../tools/temptool/sandbox_probe.go",
	} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "context.WithTimeout(context.Background()") {
				t.Errorf("%s:%d roots a sandbox context at Background, which drops the privacy connector and the workspace reach ceiling: %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Absent means allowed, which is what makes a dropped context dangerous
// rather than merely wrong. Pinned so nobody "simplifies" the default.
func TestAnAbsentCeilingReadsAsAllowedAndIsWhyTheRootMatters(t *testing.T) {
	if !netgate.WorkspaceNetworkAllowed(context.Background()) {
		t.Fatal("the default changed; the Background-root test above is now the only thing standing between a dropped context and a silent block")
	}
	blocked := netgate.WithWorkspaceNetwork(context.Background(), false)
	if netgate.WorkspaceNetworkAllowed(blocked) {
		t.Error("the ceiling does not hold when it is set")
	}
	// A child keeps it: the whole mechanism is that it rides the context.
	child, cancel := context.WithCancel(blocked)
	defer cancel()
	if netgate.WorkspaceNetworkAllowed(child) {
		t.Error("the ceiling did not survive a derived context")
	}
}
