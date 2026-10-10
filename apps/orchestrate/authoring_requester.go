package orchestrate

// Authoring answers to the owner, on every path into an agent.
//
// The direct chat surface already withheld the authoring catalog from a turn
// running as anyone but the agent's owner (runner_tool_catalog.go). The
// dispatch paths did not ask at all: a channel inbound runs AS the owner's
// account whoever actually wrote it, so an authoring agent in a group chat
// handed tool_def, update_agent and the credential tools to every member of
// that chat, auto-approved. What a non-owner sender got instead was a
// paragraph of doctrine in the prompt, which is behaviour, and this is a
// permission.
//
// The requester is carried on the context rather than re-derived per hop
// because the person who asked is the one thing a delegation loses: agent A,
// messaged by a contact, delegating to authoring agent B runs B as the owner
// with no sender at all. The mark is set once, where the sender is known, and
// every run beneath it reads it.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

type nonOwnerRequesterKey struct{}

// nonOwnerMark is what a non-owner's work carries: the conversation the
// request came from, when it came over a channel, so a reply there is still
// a reply.
type nonOwnerMark struct{ origin string }

// withNonOwnerRequester marks ctx as work somebody other than the agent's
// owner started, from origin (a recipient key, operatorRecipientKey) when
// known. The mark only ever narrows: nothing clears it, and the first origin
// stays.
func withNonOwnerRequester(ctx context.Context, origin ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if m, ok := ctx.Value(nonOwnerRequesterKey{}).(nonOwnerMark); ok {
		if m.origin != "" || len(origin) == 0 {
			return ctx
		}
	}
	m := nonOwnerMark{}
	if len(origin) > 0 {
		m.origin = strings.TrimSpace(origin[0])
	}
	return context.WithValue(ctx, nonOwnerRequesterKey{}, m)
}

// carryNonOwnerRequester puts from's mark, origin included, on to: for work
// handed to a context of its own.
func carryNonOwnerRequester(from, to context.Context) context.Context {
	if !nonOwnerRequester(from) {
		return to
	}
	return withNonOwnerRequester(to, requestOrigin(from))
}

// requestOrigin is the conversation a non-owner's request came from, or "".
func requestOrigin(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	m, _ := ctx.Value(nonOwnerRequesterKey{}).(nonOwnerMark)
	return m.origin
}

// standingGrantApplies reports whether a standing approval (an "Always allow"
// recipient, an authorized-sender grant) sends to recip without asking. Those
// are the owner's say-so for the owner's own asks. When someone else started
// the work, only the conversation they wrote from is answered on them; a
// message anywhere else waits for the owner.
func standingGrantApplies(ctx context.Context, recip string) bool {
	if !nonOwnerRequester(ctx) {
		return true
	}
	o := requestOrigin(ctx)
	return o != "" && o == recip
}

// nonOwnerRequester reports whether a non-owner started this work, at this
// hop or any above it.
func nonOwnerRequester(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(nonOwnerRequesterKey{}).(nonOwnerMark)
	return ok
}

// channelSenderIsOwner classifies a run's sender the way the dispatch path
// does for its guardrails: on the TRANSPORT handle through the bridge's own
// comparison, never on the display name, which is the sender's to choose. A
// run that names no sender and is not a channel inbound (a scheduled fire, a
// monitor wake, a delegation) has no sender to classify and answers true;
// its requester, if any, is already on the context.
//
// A channel inbound carries the bridge's own verdict (SenderIsOwner, from
// ChannelInbound.FromOwner), made where the service is known: the iMessage
// daemon clears the handle on the owner's own messages (is_from_me), so an
// empty handle is the owner there and a sender nobody could name anywhere
// else. Read off the handle here, an empty one counted as the owner on every
// service: a Teams post from an app, a Slack bot, a webhook.
func channelSenderIsOwner(agentOwner string, run AgentSyncRun) bool {
	if run.Kind == "channel" {
		// The bridge decided, knowing its service (ChannelInbound.FromOwner).
		return run.SenderIsOwner
	}
	h := strings.TrimSpace(run.SenderHandle)
	if h == "" {
		return true
	}
	link, ok := ActiveMessagingLink()
	return ok && link.IsOwnerHandle(agentOwner, h)
}

// authoringWithheldReason says why a run of an authoring agent may not carry
// the authoring catalog, or "" when it may. Owner means the account the agent
// belongs to; the seed owner and an unowned record count as everyone's, as on
// the direct path.
func authoringWithheldReason(ctx context.Context, agentOwner, runtimeUser string) string {
	if agentOwner != "" && agentOwner != seedOwner && agentOwner != runtimeUser {
		return "this run is for " + runtimeUser + ", not the agent's owner"
	}
	if nonOwnerRequester(ctx) {
		return "someone other than the owner started this run"
	}
	return ""
}

// dispatchAuthoring is the grant check every dispatch path makes before it
// appends the authoring catalog: whether to grant it, and when it is withheld
// from an agent that could author, why. The refusal is logged here; the
// caller puts it on the run's trail with noteAuthoringWithheld once the run
// has one, so the tools' absence has a stated cause instead of one the model
// guesses at.
func dispatchAuthoring(ctx context.Context, target AgentRecord, agentOwner, runtimeUser string) (grant bool, withheld string) {
	if !agentCanAuthor(target) {
		return false, ""
	}
	// The record's own owner when it names one, as the direct path compares:
	// the store a target was loaded from is not always the account it is for.
	if o := strings.TrimSpace(target.Owner); o != "" {
		agentOwner = o
	}
	if why := authoringWithheldReason(ctx, agentOwner, runtimeUser); why != "" {
		Log("[orchestrate.tools] authoring catalog WITHHELD from dispatched agent %q (%s): %s", target.Name, target.ID, why)
		return false, why
	}
	return true, ""
}

// noteAuthoringWithheld records a withheld authoring catalog on the run's
// trail, where both the owner and the model can read it.
func (t *chatTurn) noteAuthoringWithheld(why string) {
	if t == nil || why == "" {
		return
	}
	t.turnDiag("authoring_withheld", "Authoring tools are absent from this run by design: "+why+
		". Authoring answers to the owner only. Nothing is broken; the owner can make the change directly.")
}
