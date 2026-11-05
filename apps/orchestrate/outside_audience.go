package orchestrate

// People outside gohort (docs/design/outside-audience.md).
//
// An outside person's turn runs as agenthost.OutsideScope: a store of their
// own, apart from the owner's and from every other person's. That is the path
// a shared agent's recipient already takes (memory_scope.go), so their notes,
// findings, history, past sessions and threads are theirs without any layer
// learning a new namespace. What differs is what reaches them from underneath:
//
//   - the agent's saved notes, read-only, so it knows its job (the owner's
//     decision, 2026-11-05), whatever the agent's share switches say;
//   - not the owner's cortex, which is a record of the owner's own activity;
//   - not the owner's findings, which are what the agent worked out across the
//     owner's conversations. Curated knowledge (uploads, attached collections)
//     still reaches them: it is published material, not anyone's private data.
//
// And the agent has to be set to serve them (AgentRecord.OutsideAudience).

import "github.com/cmcoffee/gohort/core/agenthost"

// outsideAudience reports a turn for a person outside gohort.
func (t *chatTurn) outsideAudience() bool {
	return t != nil && agenthost.IsOutsideScope(t.user)
}

// outsideRunRefusal refuses a run for an outside person on an agent its owner
// has not set to serve people outside, or returns nil.
func outsideRunRefusal(runtimeUser string, target AgentRecord) error {
	if agenthost.IsOutsideScope(runtimeUser) && !target.OutsideAudience {
		return agenthost.ErrNotForOutsideAudience
	}
	return nil
}
