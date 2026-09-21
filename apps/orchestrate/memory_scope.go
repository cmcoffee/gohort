package orchestrate

// Whose memory, which is not the same question as whose run.
//
// One identity answered both until now: t.user was the person who typed, the
// owner of the ledger row, and the namespace every memory layer read and wrote.
// That holds exactly as long as nobody runs an agent that is not theirs.
//
// A shared agent breaks it in one direction only. The RUN is the recipient's:
// their ledger row, their activity feed, their quota, their session. The MEMORY
// is two things stacked — what they have told this agent, over what the agent
// already knew. The second half must never take the first half's writes, or a
// colleague's afternoon ends up in the owner's memory and in every other
// recipient's prompt.
//
// So the two axes are named here and the three layers each ask one question:
// may this turn READ the owner's copy of this layer. Nothing here can widen
// what a turn WRITES, which is the property that makes the whole thing safe to
// default on.
//
// The defaults are what the framework already did, which is different per
// layer. See AgentRecord.ShareHoldCortex and its neighbours.

import (
	"strings"
)

// memoryScope is the two axes, separated.
type memoryScope struct {
	// Run is who is accountable for this turn: the ledger key, the activity
	// feed, the spend. Always the acting identity, never the owner, or a run
	// somebody triggered would vanish from their own feed and file under
	// somebody else's name.
	Run string
	// Write is whose memory receives what this turn learns. Same as Run today
	// and in every foreseeable case; it is a separate field because the two
	// being equal is a fact about the current design rather than a law, and
	// the servitor per-subject scope will make them differ.
	Write string
	// Under are read-only layers beneath Write, nearest first. Empty on an
	// ordinary run.
	Under []string
}

// memoryScope reports the two axes for this turn.
func (t *chatTurn) memoryScope() memoryScope {
	if t == nil {
		return memoryScope{}
	}
	ms := memoryScope{Run: strings.TrimSpace(t.user), Write: strings.TrimSpace(t.user)}
	if owner := t.memoryUnderlay(); owner != "" {
		ms.Under = []string{owner}
	}
	return ms
}

// memoryUnderlay names the owner whose memory sits beneath this turn's own, or
// empty when there is none.
//
// Empty for the owner's own run, for a seed (the framework has no namespace to
// read), and in a clean room.
//
// NOT empty for a channel inbound, although its identity is synthetic. A
// channel run is "phantom:<chatID>", whose own namespace is blank by
// construction, and an agent put on a channel with no access to what it knows
// is the useless version of itself. That was already the behaviour and it is
// the right one: putting the agent on a channel IS the decision to let that
// channel reach it. The three switches govern it there too, which is new, and
// is the first time an owner could say otherwise.
func (t *chatTurn) memoryUnderlay() string {
	if t == nil || t.incognitoSession() {
		return ""
	}
	owner := strings.TrimSpace(t.ownerUser)
	if owner == "" {
		owner = strings.TrimSpace(t.agent.Owner)
	}
	if owner == "" || owner == seedOwner || owner == strings.TrimSpace(t.user) {
		return ""
	}
	return owner
}

// readsOwnerCortex reports whether this turn's prompt carries the owner's
// standing-activity feed.
//
// Default ON, because it already was: a granted user's own cortex namespace is
// blank, so seeding from their own store gave them nothing and the agent that
// "knows the things" did not. The switch exists so an owner whose cortex has
// become a record of their own week can stop it, not because sharing it was
// wrong.
func (t *chatTurn) readsOwnerCortex() bool {
	return t.memoryUnderlay() != "" && !t.agent.ShareHoldCortex
}

// readsOwnerReference reports whether retrieval also searches the owner's copy
// of this agent's corpus.
//
// Default ON for the same reason, and with the sharper edge of the three: this
// layer is the least deliberate thing the agent holds. It is what the agent
// inferred across the owner's conversations without being asked to. An owner
// who reads what is in it and decides it should not travel turns this on.
func (t *chatTurn) readsOwnerReference() bool {
	return t.memoryUnderlay() != "" && !t.agent.ShareHoldReference
}

// readsOwnerFacts reports whether the owner's saved notes join this turn's
// always-in-prompt block.
//
// Default OFF, because this layer has never travelled and turning it on by
// default would disclose, on every existing share, whatever came up while the
// owner was talking to the agent alone.
func (t *chatTurn) readsOwnerFacts() bool {
	return t.memoryUnderlay() != "" && t.agent.ShareMemoryExplicit
}

// readsOwnerCorpus reports whether a retrieval at this scope also searches the
// owner's own accumulated corpus for this agent.
//
// Scope-aware, because the curated/derived split IS the knowledge/memory line
// and the two answers differ. Curated chunks are documents somebody put there
// on purpose: knowledge, which travels with the agent the way its collections
// do, and is not what the memory switch is about. Derived chunks are what the
// agent worked out for itself, which is memory and is gated.
func (t *chatTurn) readsOwnerCorpus(scope ChunkScope) bool {
	if t.memoryUnderlay() == "" {
		return false
	}
	return scope == ChunkScopeCuratedOnly || t.readsOwnerReference()
}
