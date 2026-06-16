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
		// accumulates its own continuing thread under the agent. The rich
		// variant carries the status callback through to the sub-session and
		// returns the agent's produced attachments.
		res, err := app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
			AgentOwner:     in.Owner,
			RuntimeUser:    in.Owner,
			AgentKey:       in.AgentID,
			SubSessionID:   in.SessionID,
			Message:        in.Text,
			StatusCallback: in.StatusCallback,
		})
		if err != nil {
			return ChannelReply{}, err
		}
		return ChannelReply{Text: res.Text, Images: res.Images}, nil
	})
}
