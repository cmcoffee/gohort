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

	. "github.com/cmcoffee/gohort/core"
)

type nonOwnerRequesterKey struct{}

// withNonOwnerRequester marks ctx as work somebody other than the agent's
// owner started. The mark only ever narrows: nothing clears it.
func withNonOwnerRequester(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, nonOwnerRequesterKey{}, true)
}

// nonOwnerRequester reports whether a non-owner started this work, at this
// hop or any above it.
func nonOwnerRequester(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(nonOwnerRequesterKey{}).(bool)
	return v
}

// channelSenderIsOwner classifies a run's sender the way the dispatch path
// does for its guardrails: on the TRANSPORT handle through the bridge's own
// comparison, never on the display name, which is the sender's to choose. A
// run that names no sender and is not a channel inbound (a scheduled fire, a
// monitor wake, a delegation) has no sender to classify and answers true;
// its requester, if any, is already on the context. A channel inbound with no
// handle cannot be shown to be the owner, so it is not.
func channelSenderIsOwner(agentOwner string, run AgentSyncRun) bool {
	h := strings.TrimSpace(run.SenderHandle)
	if h == "" && run.Kind != "channel" {
		return true
	}
	if h == "" {
		return false
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
