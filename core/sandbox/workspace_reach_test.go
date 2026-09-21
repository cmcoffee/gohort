package sandbox

// The workspace's own reach, as the sandbox argv sees it.
//
// A workspace shares the HOST's network namespace in an ordinary turn: the
// only input to allowNetwork was the turn's privacy connector, so
// --unshare-net appeared in private mode and nowhere else. An agent meant to
// process text locally and never phone anywhere had no way to say so short of
// ForcePrivate, which takes its tools and its model with it.

import (
	"context"
	"slices"
	"testing"

	"github.com/cmcoffee/gohort/core/netgate"
)

func argvHasUnshareNet(allowNetwork bool) bool {
	return slices.Contains(bwrapArgv("/tmp/ws", "echo hi", allowNetwork), "--unshare-net")
}

// The two axes AND, and each can cut on its own.
func TestTheWorkspaceCeilingAndsWithPrivacy(t *testing.T) {
	cases := []struct {
		name            string
		ctx             context.Context
		wantOut         bool
		wantUnshareArgv bool
	}{
		{"nothing set at all", context.Background(), true, false},
		{"workspace held back",
			netgate.WithWorkspaceNetwork(context.Background(), false), false, true},
		{"privacy on, workspace unrestricted",
			netgate.WithNetworkConnector(context.Background(), netgate.NewNetworkConnector(true)), false, true},
		{"both",
			netgate.WithWorkspaceNetwork(
				netgate.WithNetworkConnector(context.Background(), netgate.NewNetworkConnector(true)), false), false, true},
	}
	for _, c := range cases {
		got := netgate.WorkspaceNetworkFrom(c.ctx)
		if got != c.wantOut {
			t.Errorf("%s: reach = %v, want %v", c.name, got, c.wantOut)
		}
		if argvHasUnshareNet(got) != c.wantUnshareArgv {
			t.Errorf("%s: --unshare-net present = %v, want %v", c.name, !c.wantUnshareArgv, c.wantUnshareArgv)
		}
	}
}

// A ceiling can only narrow. Nothing downstream may hand the workspace back a
// network the turn was denied.
func TestTheCeilingCannotWidenAPrivateTurn(t *testing.T) {
	ctx := netgate.WithNetworkConnector(context.Background(), netgate.NewNetworkConnector(true))
	ctx = netgate.WithWorkspaceNetwork(ctx, true)
	if netgate.WorkspaceNetworkFrom(ctx) {
		t.Error("a private turn got its network back from the workspace setting")
	}
}

// Absent means allowed, so a caller that never learned about this behaves
// exactly as it did before it existed.
func TestAnUnsetCeilingChangesNothing(t *testing.T) {
	if !netgate.WorkspaceNetworkAllowed(context.Background()) {
		t.Error("an unset ceiling refused")
	}
	if !netgate.WorkspaceNetworkAllowed(nil) {
		t.Error("a nil context refused")
	}
	if argvHasUnshareNet(netgate.WorkspaceNetworkFrom(context.Background())) {
		t.Error("an ordinary turn lost its network namespace")
	}
}
