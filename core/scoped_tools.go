// User tool inventory across scopes — the cross-cutting answer to "what has
// been built for me, and what is it attached to?"
//
// A user's tools live in three places and only one of them was ever listed on
// their own page: the persistent pool (theirs, listed), tools bundled onto an
// agent record (visible only to an admin), and session drafts authored mid-chat
// with persist=false (visible only inside the session that made them). Someone
// could have real, working tools they had no way to see.
//
// None of it can be read from the stores alone: agent records and chat sessions
// belong to the app that owns agents, and session drafts live in one global
// table keyed by session id with no owner on the row. So the app that has that
// knowledge registers an enumerator here, the same shape as the other seams.

package core

import (
	"errors"
	"sync"
)

// Tool scopes reported by a ScopedTool.
const (
	ScopeAgentTool   = "agent"   // bundled onto an agent record (AgentRecord.Tools)
	ScopeSessionTool = "session" // authored mid-chat with persist=false
)

// Promotion targets for a session draft — where a kept draft lands.
const (
	ScopeTargetGlobal = "global" // the user-wide pool: every one of their agents
	ScopeTargetAgent  = "agent"  // the agent record the session belongs to
)

var (
	scopedPromoteMu sync.RWMutex
	scopedPromoter  func(user, agentID, sessionID, toolName, target string) (string, error)
)

// RegisterScopedToolPromoter installs the promote implementation. Called once
// at startup by the app that owns agent records and chat sessions.
func RegisterScopedToolPromoter(fn func(user, agentID, sessionID, toolName, target string) (string, error)) {
	scopedPromoteMu.Lock()
	scopedPromoter = fn
	scopedPromoteMu.Unlock()
}

// PromoteScopedTool moves a session draft into a durable scope (see the
// ScopeTarget constants) and returns the scope actually written. Errors when no
// promoter is registered, so a caller surfaces "unavailable" rather than
// silently doing nothing to the user's tool.
func PromoteScopedTool(user, agentID, sessionID, toolName, target string) (string, error) {
	scopedPromoteMu.RLock()
	fn := scopedPromoter
	scopedPromoteMu.RUnlock()
	if fn == nil {
		return "", errors.New("tool promotion is unavailable (no agents app registered)")
	}
	return fn(user, agentID, sessionID, toolName, target)
}

// ScopedTool is one tool plus where it lives and what it's attached to — enough
// context to display it under the right heading and to act on it.
type ScopedTool struct {
	Tool  TempTool `json:"tool"`
	Scope string   `json:"scope"` // ScopeAgentTool | ScopeSessionTool

	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`

	// Session fields are set for ScopeSessionTool only.
	SessionID    string `json:"session_id,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`

	// Trial mirrors TempTool.Trial: authored mid-conversation and not yet
	// confirmed by the user. A real tool on a real agent — this only says
	// nobody has vouched for it.
	Trial bool `json:"trial,omitempty"`

	// Shadowed marks a tool whose name is already covered by a committed copy
	// elsewhere — an agent's bundled tools or the user's pool. add_tool writes
	// BOTH a session draft and a committed copy so the tool is callable
	// mid-turn, so the draft is stale duplication from that moment on.
	//
	// Reported rather than filtered because the consumers want opposite things:
	// a UI listing "what haven't I kept?" hides these, while the cleanup that
	// deletes stale drafts wants exactly these. Dropping them at the source
	// would silently disable that cleanup.
	Shadowed bool `json:"shadowed,omitempty"`
}

var (
	scopedToolMu     sync.RWMutex
	scopedToolLister func(user string) []ScopedTool
)

// RegisterScopedToolLister installs the enumerator. Called once at startup by
// the app that owns agents and chat sessions.
func RegisterScopedToolLister(fn func(user string) []ScopedTool) {
	scopedToolMu.Lock()
	scopedToolLister = fn
	scopedToolMu.Unlock()
}

// ListScopedTools returns every agent-bundled tool and session draft belonging
// to user, INCLUDING shadowed ones (see ScopedTool.Shadowed — callers filter).
// Returns nil when no lister is registered, so a deployment without the agents
// app renders nothing rather than erroring.
func ListScopedTools(user string) []ScopedTool {
	scopedToolMu.RLock()
	fn := scopedToolLister
	scopedToolMu.RUnlock()
	if fn == nil || user == "" {
		return nil
	}
	return fn(user)
}

// ListSessionDrafts is ListScopedTools narrowed to session-scoped drafts.
func ListSessionDrafts(user string) []ScopedTool {
	all := ListScopedTools(user)
	out := all[:0:0]
	for _, t := range all {
		if t.Scope == ScopeSessionTool {
			out = append(out, t)
		}
	}
	return out
}
