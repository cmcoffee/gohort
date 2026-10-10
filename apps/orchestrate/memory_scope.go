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
// layer. See AgentRecord.ShareCortex and its neighbours.
//
// Resolved rather than read off a field: an agent that has answered wins, then
// a record written before these were tri-states, then the OWNER's default for
// all agents. The owner's, not the runtime user's - a shared agent runs for
// somebody else, and which of its layers travel is a decision its owner made
// about their own agent.

import (
	"context"
	"errors"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
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
// Empty for a channel inbound too, which now runs AS the owner's account: its
// own namespace is the owner's. The three switches still govern a stranger on
// the channel, through ownLayerShared/searchOwnKnowledge below rather than
// through this underlay.
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

// strangerOnChannel reports a channel run whose sender the bridge did not
// recognize as the owner. Such a run executes AS the owner's account, so the
// underlay below is empty and the three share switches never applied to it:
// the owner's facts, cortex and derived memory reached whoever messaged.
func (t *chatTurn) strangerOnChannel() bool {
	return t != nil && t.requesterChannel != "" && !t.requesterOwnerHandle
}

// strangerMayNotForget refuses a deletion from the owner's memory on the word
// of someone else on a channel, or returns nil. Their own notes are attributed
// to them; the owner's are not theirs to remove.
func (t *chatTurn) strangerMayNotForget() error {
	if !t.strangerOnChannel() {
		return nil
	}
	return errors.New("nothing deleted: this request came from someone other than the owner on a channel, and only the owner removes what is remembered")
}

// attributedToSpeaker prefixes a finding saved on a stranger's word with who
// said it, so a later recall reads it as that person's claim rather than the
// owner's or the agent's own (a pinned note carries the same as Speaker
// fields). Unchanged for the owner.
func (t *chatTurn) attributedToSpeaker(content string) string {
	if !t.strangerOnChannel() {
		return content
	}
	who := strings.TrimSpace(t.requesterName)
	if h := strings.TrimSpace(t.requesterHandle); h != "" && h != who {
		if who == "" {
			who = h
		} else {
			who += " (" + h + ")"
		}
	}
	if who == "" {
		who = "someone on " + t.requesterChannel
	}
	return "Said by " + who + ", not the owner: " + content
}

// ownLayerShared reports whether one of the owner's own memory layers reaches
// this turn: always for the owner, and for a stranger on a channel only when
// the owner turned that layer's sharing on, the same switches somebody using
// a shared copy of the agent meets.
func (t *chatTurn) ownLayerShared(key string) bool {
	return !t.strangerOnChannel() || settingIsOn(RootDB, t.agent, key)
}

// searchOwnKnowledge searches the agent's knowledge for this turn, narrowed for
// a stranger on a channel when the owner did not share the derived (reference)
// layer: a search of derived memory finds nothing, and a search of everything
// finds only curated documents.
func (t *chatTurn) searchOwnKnowledge(ctx context.Context, topic, query string, qVec []float32, k int, skills []SkillRecord, scope ChunkScope) []SearchHit {
	if !t.ownLayerShared(defaultShareReference) {
		switch scope {
		case ChunkScopeDerivedOnly:
			return nil
		case ChunkScopeAll:
			scope = ChunkScopeCuratedOnly
		}
	}
	return searchAgentKnowledgeVec(ctx, t.app.DB, t.user, t.ownerUser, t.readsOwnerCorpus(scope), t.agent.ID, topic, query, qVec, k, skills, t.agent.AttachedCollections, scope)
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
	return t.memoryUnderlay() != "" &&
		settingIsOn(RootDB, t.agent, defaultShareCortex)
}

// readsOwnerReference reports whether retrieval also searches the owner's copy
// of this agent's corpus.
//
// Default ON for the same reason, and with the sharper edge of the three: this
// layer is the least deliberate thing the agent holds. It is what the agent
// inferred across the owner's conversations without being asked to. An owner
// who reads what is in it and decides it should not travel turns this on.
func (t *chatTurn) readsOwnerReference() bool {
	return t.memoryUnderlay() != "" &&
		settingIsOn(RootDB, t.agent, defaultShareReference)
}

// readsOwnerFacts reports whether the owner's saved notes join this turn's
// always-in-prompt block.
//
// Default OFF, because this layer has never travelled and turning it on by
// default would disclose, on every existing share, whatever came up while the
// owner was talking to the agent alone.
func (t *chatTurn) readsOwnerFacts() bool {
	return t.memoryUnderlay() != "" &&
		settingIsOn(RootDB, t.agent, defaultShareNotes)
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
