package sandbox

// The command's own declaration that it needs raw TCP.
//
// RawNetwork was documented for a long time and consulted by nothing: every
// comment here, the pydeps note and the tool-authoring help all said a shell
// command runs with --unshare-net unless it declares raw_network=true, while
// the code asked only the privacy connector. So an author who did NOT set the
// flag believed their tool could not reach the network, and it could.
//
// Closing that by default would break every existing tool that curls without
// having declared it, so the deployment throws the switch.

import (
	"context"
	"slices"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

func withClosedDefault(t *testing.T, closed bool) {
	t.Helper()
	prev := ShellNetworkClosedByDefault
	ShellNetworkClosedByDefault = func() bool { return closed }
	t.Cleanup(func() { ShellNetworkClosedByDefault = prev })
}

func netAllowed(t *testing.T, ctx context.Context, raw bool) bool {
	t.Helper()
	c, err := buildSandboxedShellCmd(ctx, ShellRun{
		Command: "echo hi", WorkspaceDir: t.TempDir(), RawNetwork: raw,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return !slices.Contains(c.Cmd.Args, "--unshare-net")
}

// Until the deployment switches, nothing changes: the declaration is recorded
// and not enforced, so an undeclared tool keeps the network it has always had.
func TestTheDeclarationIsNotEnforcedUntilTheDeploymentSaysSo(t *testing.T) {
	withClosedDefault(t, false)
	for _, raw := range []bool{false, true} {
		if !netAllowed(t, context.Background(), raw) {
			t.Errorf("raw=%v lost its network while the default is open", raw)
		}
	}
}

// Once switched, the declaration is the gate.
func TestWithTheDefaultClosedOnlyADeclaredCommandReachesOut(t *testing.T) {
	withClosedDefault(t, true)
	if netAllowed(t, context.Background(), false) {
		t.Error("an undeclared command kept the host's network namespace")
	}
	if !netAllowed(t, context.Background(), true) {
		t.Error("a command that declared raw network was denied it")
	}
}

// It NARROWS only. A declaration cannot hand back what privacy mode or the
// agent's workspace ceiling took away — that ordering is the whole reason the
// ceilings are ceilings.
func TestADeclarationCannotWidenACeiling(t *testing.T) {
	withClosedDefault(t, true)
	private := netgate.WithNetworkConnector(context.Background(), netgate.NewNetworkConnector(true))
	if netAllowed(t, private, true) {
		t.Error("a declared command got network back on a private turn")
	}
	held := netgate.WithWorkspaceNetwork(context.Background(), false)
	if netAllowed(t, held, true) {
		t.Error("a declared command got network back on a workspace that may not dial")
	}
}
