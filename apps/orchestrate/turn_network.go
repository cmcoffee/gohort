// The two network facts every agent run carries, assembled in one place.
//
// There are two of them and they are answered from different stores, which is
// how a run path came to carry one and not the other. The connector is the
// turn's privacy cutoff - may this turn reach out at all - and it is mutable,
// because the privacy endpoint flips it mid-turn. The workspace ceiling is a
// standing fact about one agent - may code running in its sandbox be the thing
// dialling - and it does not move for the life of the turn.
//
// BOTH read as ALLOWED when absent (netgate.WorkspaceNetworkAllowed returns
// true for a context nobody stamped; a nil connector is an open one). That is
// the right default for a deployment that has never set either, and the wrong
// failure mode for a run path that simply forgot: the omission does not show
// up as a missing restriction, it shows up as extra reach. A scheduled fire
// built its own context by hand and stamped neither, so an agent whose
// workspace was blocked from the network dialled out freely on exactly the
// surface with nobody watching it, and a ForcePrivate agent ran with no
// runtime floor under its tools.
//
// So: one function, both facts, and a caller that cannot take half of it.

package orchestrate

import (
	"context"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
)

// withAgentNetwork stamps ctx with this agent's network state and hands back
// the connector, which the caller must also hang on the session: NetworkAllowed
// reads the SESSION field and the workspace ceiling reads the CONTEXT, and
// ToolSession.WorkspaceNetworkAllowed ANDs the two. One of them set is half a
// gate.
//
// private is the turn's own answer ORed with the agent's - a live turn has a
// user toggle to fold in, an unattended one has only the agent's ForcePrivate -
// so it is passed rather than derived. user is the fallback identity for an
// agent record carrying no owner; the ceiling itself resolves against the
// OWNER's defaults, never the visitor's.
func withAgentNetwork(ctx context.Context, user string, agent AgentRecord, private bool) (context.Context, *NetworkConnector) {
	conn := NewNetworkConnector(private)
	ctx = WithNetworkConnector(ctx, conn)
	ctx = netgate.WithWorkspaceNetwork(ctx, agentWorkspaceNetwork(RootDB, agentDefaultsOwner(agent, user), agent))
	return ctx, conn
}
