package orchestrate

// Ask me before every call, per agent, and through delegation.
//
// The owner marks a tool to ask first in three places: on the tool itself
// (Extensions › Tools › Security, every agent), or on one agent (its Tools
// section), where it asks only when that agent calls it. web_search on the
// research agent shows the owner each query; the same tool on another agent
// runs freely.
//
// A run one agent hands to another carries the asks with it. If the caller
// asks before web_search, the agent it delegated to asks too: otherwise
// delegating would be the way around the owner's mark.
//
// The answer reaches the agent loop as AgentLoopConfig.AskFirst, because the
// loop only ever confirmed tools that declare NeedsConfirm: a mark on a tool
// that asks nothing on its own (web_search) was stored and never consulted.

import (
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// asksFirst reports whether a call of name stops for the owner on this turn:
// this agent's mark, the fleet-wide one, or the mark of any agent that handed
// this run over (askChain).
func (t *chatTurn) asksFirst(name string) bool {
	if t == nil || AuthDB == nil || strings.TrimSpace(name) == "" {
		return false
	}
	db := AuthDB()
	if db == nil {
		return false
	}
	_, owner := t.ownerView()
	if owner == "" {
		owner = t.user
	}
	if UserToolAsksInChat(db, owner, t.agent.ID, name) {
		return true
	}
	for _, id := range t.askChain {
		if id != "" && UserToolAsksInChat(db, owner, id, name) {
			return true
		}
	}
	return false
}

// delegatedAskChain is the ask chain for a run t hands to another agent: t's
// own chain and t's agent.
func (t *chatTurn) delegatedAskChain() []string {
	if t == nil {
		return nil
	}
	out := append([]string{}, t.askChain...)
	if t.agent.ID != "" {
		out = append(out, t.agent.ID)
	}
	return out
}

// sessionAllows is the tools the owner allowed "for this session", by owner,
// conversation and tool. In memory: a restart asks again, the safe direction.
var sessionAllows sync.Map // key -> true

// conversation is the conversation this turn answers to: its own, or, for a
// run another agent handed over inside a chat, that chat's (askConversation).
func (t *chatTurn) conversation() string {
	if t == nil {
		return ""
	}
	if t.session != nil && t.session.ID != "" {
		return t.session.ID
	}
	return t.askConversation
}

// sessionAllowKey names "tool name in this conversation" for this turn's
// owner; "" when the turn answers to no conversation, which offers no
// session-wide yes.
func (t *chatTurn) sessionAllowKey(name string) string {
	conv := t.conversation()
	if conv == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	_, owner := t.ownerView()
	if owner == "" {
		owner = t.user
	}
	return owner + "\x00" + conv + "\x00" + name
}

// allowForSession records an "Allow for this session".
func allowForSession(key string) {
	if key != "" {
		sessionAllows.Store(key, true)
	}
}

// sessionAllowed reports whether the owner allowed this for the session.
func sessionAllowed(key string) bool {
	if key == "" {
		return false
	}
	_, ok := sessionAllows.Load(key)
	return ok
}
