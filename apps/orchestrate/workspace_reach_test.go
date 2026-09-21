package orchestrate

// The per-agent ceiling on what its workspace may dial.
//
// ForcePrivate was the only way to say "no network", and it takes the agent's
// tools and its model with it: right for a compliance bot, useless for
// "process this text locally and do not phone anywhere from in there".

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

// readRepoFile reads a source file outside this package, for the guards that
// are about two files agreeing.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// It inherits DOWNWARD and only narrows, which is the direction every
// restriction here inherits. Building a sub-agent is otherwise how you launder
// one.
func TestTheCeilingInheritsDownADispatch(t *testing.T) {
	open := AgentRecord{ID: "child"}
	closed := AgentRecord{ID: "child", WorkspaceNoNetwork: true}

	// A parent that may dial, dispatching to a child that may not.
	ctx, _ := applyForcePrivateToDispatch(context.Background(), nil, nil, closed)
	if netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("the child's own ceiling was ignored")
	}

	// A parent that may NOT dial, dispatching to a child with no ceiling of
	// its own: the child inherits it and cannot widen.
	parent := netgate.WithWorkspaceNetwork(context.Background(), false)
	ctx, _ = applyForcePrivateToDispatch(parent, nil, nil, open)
	if netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("a sub-agent laundered its parent's ceiling")
	}

	// Neither restricted: unchanged.
	ctx, _ = applyForcePrivateToDispatch(context.Background(), nil, nil, open)
	if !netgate.WorkspaceNetworkAllowed(ctx) {
		t.Error("an unrestricted dispatch lost its network")
	}
}

// Both doors out of the sandbox. Closing the namespace and leaving the proxy
// hook open would just move a script from curl to gohort.fetch, which is the
// route the sandbox docs tell it to prefer.
func TestBothWaysOutAreClosed(t *testing.T) {
	src := readRepoFile(t, "../../core/sandbox_hook.go")
	if !strings.Contains(src, "!h.Sess.WorkspaceNetworkAllowed()") {
		t.Error("the gohort.fetch hook does not honour the workspace ceiling")
	}
	exec := readRepoFile(t, "../../core/sandbox/exec.go")
	if !strings.Contains(exec, "netgate.WorkspaceNetworkFrom(ctx)") {
		t.Error("the sandbox namespace does not honour the workspace ceiling")
	}
	if strings.Contains(exec, "allowNetwork := netgate.NetworkAllowedFromContext(ctx)") {
		t.Error("the namespace reads the privacy connector alone again")
	}
}

// The setting is on screen and saveable. A toggle that stores nothing is worse
// than no toggle.
func TestTheCeilingIsOfferedAndSaveable(t *testing.T) {
	src := packageSource(t)
	if !strings.Contains(src, `Field: "workspace_no_network", Type: "toggle"`) {
		t.Error("the setting is not offered in the editor")
	}
	if !patchAgentFields["workspace_no_network"] {
		t.Error("the setting is offered but the PATCH allowlist drops it")
	}
	// Set where the turn's context is assembled, or nothing downstream sees it.
	if !strings.Contains(src, "netgate.WithWorkspaceNetwork(ctx, !agent.WorkspaceNoNetwork)") {
		t.Error("the live turn never attaches the ceiling")
	}
}
