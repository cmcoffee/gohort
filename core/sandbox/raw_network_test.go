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
	"strings"
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

// The curl and wget stand-ins in the shim bin dir decide between the real
// binary and gohort's fetch hook by GOHORT_NETWORK, so it has to state the
// decision made here in every case: a stand-in that thought the network was
// open would hand an agent a curl that cannot resolve anything, and one that
// thought it closed would translate a raw_network tool's curl it should leave
// alone.
func TestTheRunSaysWhetherItHasANetwork(t *testing.T) {
	stated := func(ctx context.Context, raw bool) string {
		t.Helper()
		c, err := buildSandboxedShellCmd(ctx, ShellRun{Command: "true", WorkspaceDir: t.TempDir(), RawNetwork: raw})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		args := c.Cmd.Args
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--setenv" && args[i+1] == "GOHORT_NETWORK" {
				return args[i+2]
			}
		}
		for _, kv := range c.Cmd.Env {
			if v, ok := strings.CutPrefix(kv, "GOHORT_NETWORK="); ok {
				return v
			}
		}
		return ""
	}
	private := netgate.WithNetworkConnector(context.Background(), netgate.NewNetworkConnector(true))
	withClosedDefault(t, true)
	for name, c := range map[string]struct {
		ctx  context.Context
		raw  bool
		want string
	}{
		"closed default":       {context.Background(), false, "closed"},
		"declared raw network": {context.Background(), true, "open"},
		"raw network, private": {private, true, "closed"},
	} {
		if got := stated(c.ctx, c.raw); got != c.want {
			t.Errorf("%s: GOHORT_NETWORK=%q, want %q", name, got, c.want)
		}
	}
	withClosedDefault(t, false)
	if got := stated(context.Background(), false); got != "open" {
		t.Errorf("open default: GOHORT_NETWORK=%q, want open", got)
	}
}
