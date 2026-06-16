// Channel agent runner. core (core/channel.go) owns the Channel store and the
// inbound→agent seam; this is the agent-aware half: when a message arrives on
// a bound Channel, the transport (phantom) calls core.RunChannelAgent, which
// dispatches here to run the bound agent in a per-contact session and return
// its reply for the transport to deliver. Mirrors registerStandingRunner.
// See docs/channels-and-agents.md.

package orchestrate

import (
	"context"

	. "github.com/cmcoffee/gohort/core"
)

// registerChannelAgentRunner installs the closure core invokes to run a
// channel's bound agent on one inbound message. Call once at startup.
func registerChannelAgentRunner(app *OrchestrateApp) {
	RegisterChannelAgentRunner(func(ctx context.Context, in ChannelInbound) (ChannelReply, error) {
		// agentOwner == runtimeUser: the channel owner's agent runs under the
		// owner's own store. SessionID is per-contact (stable), so each contact
		// accumulates its own continuing thread under the agent. Empty
		// injectionQueueID + freshSession=false → continue the session.
		reply, err := app.RunAgentSyncContinuing(ctx, in.Owner, in.Owner, in.AgentID, in.SessionID, "", in.Text, false)
		if err != nil {
			return ChannelReply{}, err
		}
		return ChannelReply{Text: reply}, nil
	})
}
