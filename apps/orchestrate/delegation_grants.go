package orchestrate

// The user's standing decision about one agent handing work to another (the
// Permissions pane's Allow, Ask, Block), read the same way by both routes an
// agent hands work by: delegate and agents(run).
//
// Keyed by the target's id. An agent keeps its id across renames and edits,
// while a grant keyed by the name the model typed when the owner pressed
// Always allow stopped matching the first time the model typed it differently
// ("comedian", or the id), and an agent the owner had allowed was queued for
// approval again. Grants written before this carry a name, so a name (in any
// case) still matches, but a new one is written under the id.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// delegationDecisionFor is the decision on from handing work to target, and
// whether one was made. typed is the text the call named the target by, for
// a grant written under it. The requester's own decision comes before the
// all-agents row, as in DelegationPolicy, and within each, the id before a
// name.
func delegationDecisionFor(owner, from string, target AgentRecord, typed string) (string, bool) {
	id := strings.TrimSpace(target.ID)
	names := []string{strings.TrimSpace(target.Name), strings.TrimSpace(typed)}
	entries := ListDelegationPolicies(RootDB, owner)
	scopes := []string{strings.TrimSpace(from)}
	if scopes[0] != "" {
		scopes = append(scopes, "")
	}
	for _, scope := range scopes {
		var byName string
		for _, e := range entries {
			if e.Scope != scope {
				continue
			}
			if id != "" && e.Target == id {
				return e.Policy, true
			}
			if byName == "" {
				for _, n := range names {
					if n != "" && strings.EqualFold(e.Target, n) {
						byName = e.Policy
					}
				}
			}
		}
		if byName != "" {
			return byName, true
		}
	}
	return "", false
}

// delegationGrantKey is what a new grant for agent is written under: its id
// when the name resolves to one agent, else the text as given.
func delegationGrantKey(owner, agent string) string {
	if rec, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agent); ok && rec.ID != "" {
		return rec.ID
	}
	return strings.TrimSpace(agent)
}

// delegateGate checks a delegation the way agents(run) checks a dispatch, and
// returns the agent it names.
type delegateGate func(agent, brief string) (AgentRecord, error)

type delegateGateKey struct{}

func delegateGateFrom(ctx context.Context) delegateGate {
	g, _ := ctx.Value(delegateGateKey{}).(delegateGate)
	return g
}

// gateDelegate hands the delegate tool among tools this turn's dispatch
// checks (agentsRunGate, which has no side effects). The delegate tool is
// built where there is no turn to ask, and asking the user was the only check
// it made: so a delegation now runs without asking only where these hold.
func (t *chatTurn) gateDelegate(tools []AgentToolDef) []AgentToolDef {
	for i := range tools {
		if tools[i].Tool.Name != "delegate" {
			continue
		}
		h := tools[i].Handler
		gate := delegateGate(func(agent, brief string) (AgentRecord, error) {
			target, _, err := t.agentsRunGate(map[string]any{"agent": agent, "message": brief})
			return target, err
		})
		tools[i].Handler = func(ctx context.Context, args map[string]any) (string, error) {
			// Builder from a turn nobody watches waits for the owner, by this
			// door as by agents(run) (holdBuilderRun).
			if note, held := t.holdBuilderRun(ctx, map[string]any{"agent": oArgStr(args, "agent"), "message": oArgStr(args, "brief")}); held {
				return note, nil
			}
			return h(context.WithValue(ctx, delegateGateKey{}, gate), args)
		}
	}
	return tools
}

// holdBuilderRun queues a run of Builder from a turn nobody is watching (a
// channel message, a wake, a schedule) for the owner's approval, and reports
// whether it did. Run there, Builder made new things but every edit it needed
// to something that existed queued on its own and was refused, and approving
// those later redid nothing, so the work was lost. Held, the request is one
// approval: approving it runs Builder in the background with its edits allowed
// (withOwnerApprovedRun), and the result comes back to the conversation that
// asked. An explicit Allow for Builder still runs at once; Block and anything
// else the gate refuses are left to the tool to report.
func (t *chatTurn) holdBuilderRun(ctx context.Context, args map[string]any) (string, bool) {
	if t.chatAsker() != nil {
		return "", false
	}
	target, msg, err := t.agentsRunGate(args)
	if err != nil || !isBuilderAgent(target.ID) {
		return "", false
	}
	_, owner := t.fleetView()
	if policy, decided := delegationDecisionFor(owner, t.agent.ID, target, stringArg(args, "agent")); decided && policy != PolicyAsk {
		return "", false
	}
	a, qctx := t.queueDispatch(ctx, owner, target, msg)
	return fmt.Sprintf("Not run yet: Builder changes things, and nobody is here to approve its changes, so the request is queued for the user's approval (id %s). Once they approve it, Builder does it in the background with those changes allowed. %s%s Tell them it is waiting on their approval and what it will do.",
		a.ID, requestOriginNote(a), privateDelegationNote(qctx, false)), true
}

// queueDispatch saves a run of target for the owner's approval, with where it
// was asked from so the approved run reports back there.
func (t *chatTurn) queueDispatch(ctx context.Context, owner string, target AgentRecord, msg string) (Authorization, context.Context) {
	sess := ToolSessionFromContext(ctx)
	a := Authorization{Owner: owner, Agent: chFirst(target.Name, target.ID), Brief: msg, FromAgent: t.agent.ID}
	qctx := stampRequestOrigin(&a, sess)
	a = SaveAuthorization(RootDB, a)
	if sess != nil && sess.PendingApprovalPrompt != nil {
		sess.PendingApprovalPrompt(a)
	}
	return a, qctx
}

// queueAskedDispatch queues agents(run) for the user's approval when they set
// its target to Ask, as delegate does, and reports whether it did. Until this
// only delegate read Ask, so an agent told to wait could reach the same agent
// by the other tool. An agent's own sub-agent is exempt, as it is from Block:
// the parent is the only one that can call it.
//
// Anything the gate refuses is left to agentsRunActionFor to report.
func (t *chatTurn) queueAskedDispatch(ctx context.Context, args map[string]any) (string, bool) {
	if note, held := t.holdBuilderRun(ctx, args); held {
		return note, true
	}
	target, msg, err := t.agentsRunGate(args)
	if err != nil || ownSubAgent(t.agent, target) {
		return "", false
	}
	_, owner := t.fleetView()
	policy, decided := delegationDecisionFor(owner, t.agent.ID, target, stringArg(args, "agent"))
	if !decided || policy != PolicyAsk {
		return "", false
	}
	a, qctx := t.queueDispatch(ctx, owner, target, msg)
	return fmt.Sprintf("Not run yet: the user set %q to Ask, so it is queued for their approval in the Authorizations pane (id %s) and runs once approved. Tell them it is waiting. %s%s",
		a.Agent, a.ID, requestOriginNote(a), privateDelegationNote(qctx, false)), true
}
