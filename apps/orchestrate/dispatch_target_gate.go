// What the agent being CALLED will accept, asked the same way whatever route
// the call arrived by.
//
// There were three routes and one of them asked. agents(run) checked the
// target's inbound policy and the user's delegation block; a machine's
// delegating step and a pipeline's agent stage both resolved a target by name
// and dispatched straight to it. So an agent that accepts no inbound
// dispatches was still reachable by authoring a machine or a pipeline that
// names it - and since Builder can author both, that is the laundering route
// the inbound gate exists to close.
//
// The two checks here are the ones that belong to the TARGET. The caller's own
// dispatch policy (Allow none, an allowlist, the Builder carve-out) stays where
// it is: it is about the caller, its refusals each say something different, and
// agents(run) is the only surface that has one. What a target accepts is not
// like that - it is one fact about one agent, and a route that skips it is not
// a different policy, it is the policy not being enforced.
//
// A run with NO calling agent is not gated. Inbound mode answers "which agents
// may call me"; a machine started from its own page, or a pipeline launched by
// the person who owns it, has a human on the other end, and a rule about agents
// must not quietly become a rule about the owner reaching their own fleet.

package orchestrate

import (
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

// targetAcceptsDispatch returns "" when caller may reach target, or the
// refusal to report when it may not.
//
// owner is whose fleet both agents belong to - the delegation block is recorded
// per (owner, caller, target), so it is read with the fleet's identity rather
// than the runtime user's, the same as every other read of it.
func targetAcceptsDispatch(owner string, caller, target AgentRecord) string {
	if caller.ID == "" {
		return ""
	}
	// The user's own Block on this target, which is about the TARGET and so
	// holds whatever route reached it. This was already honoured by the
	// Operator's delegate tool and by agents(run), and by neither of the two
	// routes below - "blocked" governed the surfaces that thought to ask.
	if IsDelegationBlocked(RootDB, owner, caller.ID, target.Name) ||
		IsDelegationBlocked(RootDB, owner, caller.ID, target.ID) {
		return fmt.Sprintf("delegation to %q is BLOCKED in the user's permission settings, so this call was refused. "+
			"Do NOT retry and do NOT route around it; only the user can change this in the Permissions pane.", agentName(target))
	}
	// What the target accepts. A sub-agent's parent is exempt inside
	// inboundAllows: ownership IS the link, and a rule that locked a parent
	// out of its own child would strand the child with no way to be reached.
	if !inboundAllows(target, caller) {
		return inboundRefusal(target)
	}
	return ""
}
