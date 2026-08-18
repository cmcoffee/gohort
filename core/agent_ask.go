// Asking an agent one question, from an app that is not orchestrate.
//
// Apps hold their own model handle (AppCore.LLM) and use it directly, which is
// right for work the app itself defines — a techwriter rewrite, a codewriter
// edit. It is the wrong tool the moment the app wants something only an AGENT
// knows: an agent is its prompt PLUS its memory, its attached sources, its
// collections and its tools, and none of that is reachable through a bare model
// call. An app asking the raw model about a domain gets a confident summary of
// nothing in particular.
//
// So this is the seam for "ask agent X this question and give me its answer" —
// one shot, no session to manage, no orchestrate import. Same shape as the
// standing-runner and channel-runner seams: core declares it, the agent-aware
// package (orchestrate) installs it at startup, and callers degrade cleanly in
// a deployment where that package isn't loaded.
//
// Deliberately NOT a streaming or continuing interface. A caller that needs a
// conversation wants a real session with its own history and cancellation, and
// building that on top of a one-shot helper produces a worse version of what
// orchestrate already has. This is for a question with an answer.
package core

import (
	"context"
	"fmt"
	"sync"
)

// AgentAskFunc runs one question against an agent and returns its reply.
// owner is the user whose agent it is (and whose store the run reads);
// agentID is the agent's stable id or name.
type AgentAskFunc func(ctx context.Context, owner, agentID, question string) (string, error)

var (
	agentAskMu sync.RWMutex
	agentAsk   AgentAskFunc
)

// RegisterAgentAsk installs the one-shot agent dispatcher. Called once at
// startup by the agent-aware package.
func RegisterAgentAsk(fn AgentAskFunc) {
	agentAskMu.Lock()
	agentAsk = fn
	agentAskMu.Unlock()
}

// AgentAskReady reports whether an agent-aware package has installed a
// dispatcher — so a caller can hide a feature that cannot work rather than
// offering a button that always errors.
func AgentAskReady() bool {
	agentAskMu.RLock()
	defer agentAskMu.RUnlock()
	return agentAsk != nil
}

// AskAgent runs one question against an agent and returns its answer.
//
// The error when nothing is registered names the CAUSE rather than reporting a
// generic failure: "agent dispatch is unavailable" is a deployment fact the
// operator can act on, while "request failed" sends them looking at the agent.
func AskAgent(ctx context.Context, owner, agentID, question string) (string, error) {
	agentAskMu.RLock()
	fn := agentAsk
	agentAskMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("agent dispatch is unavailable in this deployment")
	}
	if owner == "" || agentID == "" {
		return "", fmt.Errorf("owner and agent are both required to ask an agent")
	}
	return fn(ctx, owner, agentID, question)
}

// AgentChoice is one agent offered to a user picking one. Deliberately three
// display fields and nothing else: an app choosing an agent needs to show the
// choice and record it, and every further property is orchestrate's business.
type AgentChoice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// AgentListFunc returns the agents a user may pick from.
type AgentListFunc func(owner string) []AgentChoice

var (
	agentListMu sync.RWMutex
	agentList   AgentListFunc
)

// RegisterAgentList installs the agent enumerator. Installed alongside the
// dispatcher: an app that can ASK an agent has to let its user say which one,
// and a picker with no list is a feature nobody can turn on.
func RegisterAgentList(fn AgentListFunc) {
	agentListMu.Lock()
	agentList = fn
	agentListMu.Unlock()
}

// ListAgentsFor returns the agents this user can pick. Empty (never nil-panics)
// when no agent-aware package is loaded, so a host renders an empty picker
// rather than failing to render at all.
func ListAgentsFor(owner string) []AgentChoice {
	agentListMu.RLock()
	fn := agentList
	agentListMu.RUnlock()
	if fn == nil || owner == "" {
		return nil
	}
	return fn(owner)
}
