package orchestrate

// Who handed this run over, on every kind of hand-off.
//
// An agents(run) chain carried three limits: a depth cap (maxDispatchDepth),
// a cycle guard, and transitive authority (a run cannot reach further than the
// agent at the root of its chain allows). They lived on the sub-turn, which
// only the inline path built from its caller. A delegate, a detached run, a
// machine step's delegate and a channel run each started a fresh turn knowing
// none of it, so delegate -> delegate -> delegate had no depth at all, could
// loop, and shed the root's allow-list at the first such hop. Its guardrails
// too: only the agent immediately above was read.
//
// So the lineage rides on the CONTEXT, which every hand-off passes on: the
// agents on the call path, root first, ending with the run's own agent, and
// the authority at the root. A hand-off reads it, adds what its own via names,
// checks the three limits, and stamps the run it starts.

import (
	"context"
	"fmt"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

type dispatchLineageKey struct{}

type dispatchLineage struct {
	chain  []string           // the agents on the call path, root first, ending with the running agent
	origin *dispatchAuthority // the authority at the root; nil when the root set none
}

// withDispatchLineage marks ctx as running under chain (ending with the
// running agent) and origin.
func withDispatchLineage(ctx context.Context, chain []string, origin *dispatchAuthority) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, dispatchLineageKey{}, dispatchLineage{chain: append([]string(nil), chain...), origin: origin})
}

// dispatchLineageFrom is the lineage ctx carries, or none.
func dispatchLineageFrom(ctx context.Context) ([]string, *dispatchAuthority) {
	if ctx == nil {
		return nil, nil
	}
	l, _ := ctx.Value(dispatchLineageKey{}).(dispatchLineage)
	return append([]string(nil), l.chain...), l.origin
}

// carryDispatchLineage puts from's lineage on to, for a hand-off that builds
// its context from somewhere else (a session's) rather than from the call's.
// A chain that may not think (no_thinking.go) stays that way too.
func carryDispatchLineage(from, to context.Context) context.Context {
	if !chainThink(from, true) {
		to = agenthost.WithoutThinking(to)
	}
	chain, origin := dispatchLineageFrom(from)
	if len(chain) == 0 {
		return to
	}
	return withDispatchLineage(to, chain, origin)
}

// agentAuthority is what an agent's own dispatch policy allows, as the root
// of a chain.
func agentAuthority(a AgentRecord) *dispatchAuthority {
	return &dispatchAuthority{
		AgentID:   a.ID,
		AgentName: a.Name,
		Mode:      effectiveDispatchMode(a),
		Targets:   append([]string(nil), a.AllowedDispatchTargets...),
	}
}

// handOffLineage is the chain of agents above target on this hand-off and the
// authority at its root, or why the hand-off is refused. via names the agents
// the caller says handed it over, added to what ctx already carries.
//
// The limits are the ones an agents(run) chain is held to: no agent twice on
// the path, no deeper than maxDispatchDepth, and past the first hop nothing
// the root's policy does not reach, except the calling agent's own sub-agent.
func handOffLineage(ctx context.Context, ownerDB Database, target AgentRecord, via ...string) ([]string, *dispatchAuthority, error) {
	chain, origin := dispatchLineageFrom(ctx)
	for _, id := range via {
		if id != "" && !containsString(chain, id) {
			chain = append(chain, id)
		}
	}
	if len(chain) == 0 {
		return nil, nil, nil
	}
	if containsString(chain, target.ID) {
		return nil, nil, fmt.Errorf("dispatch cycle: %q is already on the call chain for this run (%d agents up). Answer directly, or hand it to a different agent", agentName(target), len(chain))
	}
	if len(chain) > maxDispatchDepth {
		return nil, nil, fmt.Errorf("dispatch depth limit %d exceeded: %d agents have already handed this along. Do the work with your own tools, or report back what is still needed", maxDispatchDepth, len(chain))
	}
	if origin == nil {
		if root, ok := loadAgent(ownerDB, chain[0]); ok {
			origin = agentAuthority(root)
		}
	}
	if caller := chain[len(chain)-1]; len(chain) >= 2 && origin != nil && target.OwnedBy != caller && !origin.allows(target) {
		return nil, nil, fmt.Errorf("%q is not reachable on this hand-off: this run is on behalf of %q, whose dispatch policy does not permit it, and a delegated agent cannot reach further than the agent that started the chain. Do what you can with your own tools, or report back that %q was needed",
			agentName(target), origin.AgentName, agentName(target))
	}
	return chain, origin, nil
}

// inheritChainGuardrails binds target by the guardrails of every agent above
// it on the chain, read from the owner's store. Accumulates like an
// agents(run) chain does, where each sub-turn's record already carries its
// parent's.
func inheritChainGuardrails(ownerDB Database, chain []string, target AgentRecord) AgentRecord {
	for _, id := range chain {
		if id == target.ID {
			continue
		}
		if from, ok := loadAgent(ownerDB, id); ok {
			target = inheritDelegatorGuardrails(from, target)
		}
	}
	return target
}
