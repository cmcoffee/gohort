// Channel agent runner. core (core/channel.go) owns the Channel store and the
// inbound→agent seam; this is the agent-aware half: when a message arrives on
// a bound Channel, the transport (phantom) calls core.RunChannelAgent, which
// dispatches here to run the bound agent in a per-contact session and return
// its reply for the transport to deliver. Mirrors registerStandingRunner.
// See docs/channels-and-agents.md.

package orchestrate

import (
	"context"
	"encoding/base64"
	"strings"

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
		// Session title = the conversation/room name (what's editable on the
		// transport side); per-message sender = this inbound's author. They
		// coincide for 1:1 but diverge for group rooms. Fall back to the sender
		// when the transport didn't supply a conversation name.
		title := in.ConversationName
		if title == "" {
			title = in.SenderName
		}
		// Decode inbound photos (base64 on the wire) to the raw bytes the
		// vision model takes; skip any that don't decode rather than failing
		// the whole turn.
		var images [][]byte
		for _, b64 := range in.Images {
			if data, derr := base64.StdEncoding.DecodeString(b64); derr == nil {
				images = append(images, data)
			}
		}
		res, err := app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
			AgentOwner:     in.Owner,
			RuntimeUser:    in.Owner,
			AgentKey:       in.AgentID,
			SubSessionID:   in.SessionID,
			Title:          title,
			MessageSender:  in.SenderName,
			Message:        in.Text,
			Images:         images,
			Interactive:    true, // a real person is texting — no delegation marker
			// Replying BACK to this same conversation is in-thread, not a
			// proactive reach-out — so it skips the send approval gate.
			ReplyAuthorizedKey: operatorRecipientKey(in.ChatID, in.Handle),
			StatusCallback:     in.StatusCallback,
		})
		if err != nil {
			return ChannelReply{}, err
		}
		// Strip framework-internal markers at the channel boundary — the same
		// safety net phantom applies on its outbox (phantom.go) and the web loop
		// applies on its reply (runner.go). Without it, a leaked delivery marker
		// ([ATTACH: …]) or a <gohort-meta> note rides out verbatim in the text.
		// Attachments for channels travel via res.Images (workspace attach), so
		// stripping the textual marker here doesn't drop a real attachment.
		replyText := StripMetaTags(res.Text)
		// Cortex feed (received → cortex): mirror this inbound into the bound
		// agent's cortex as a non-triggering observation, so the standing thread
		// stays aware of everything coming in over its channels. No-op when the
		// agent has Cortex off. The agent ALSO replied in its per-contact thread
		// (above); this is just awareness, not a second run.
		obs := strings.TrimSpace(in.Text)
		if rt := strings.TrimSpace(replyText); rt != "" {
			obs = strings.TrimSpace(obs + "\n↳ replied: " + truncateObs(rt, 200))
		}
		app.AppendCortexObservation(in.Owner, in.AgentID, chFirst(in.SenderName, in.ConversationName, "someone"), cortexKindMessage, obs)
		return ChannelReply{Text: replyText, Images: res.Images}, nil
	})
}

// RenameChannelSession retitles a channel room's per-contact session under its
// bound agent. The transport (phantom) calls this when the conversation's
// display name is edited, so the Agency rail and transcript title track the
// name set on the messaging side — the channel's title is owned by the
// transport, not the web rail. No-op if the user/agent/session can't resolve.
func (T *OrchestrateApp) RenameChannelSession(owner, agentID, chatID, name string) {
	if T == nil || T.DB == nil || owner == "" || agentID == "" || chatID == "" {
		return
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return
	}
	renameChatSession(udb, agentID, "chan:"+chatID, name)
}
