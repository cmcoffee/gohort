package docs

// Handing a publish to an AGENT instead of to an API.
//
// Two of the three destination kinds write over HTTP: a Confluence page, a
// webhook post. The third writes by asking somebody. A deployment where
// "publish this" means filing a ticket, opening a pull request, or posting to a
// channel with a house format has no endpoint to point at — it has a job
// description, and the thing that already knows how to carry out a job
// description is an agent.
//
// This does NOT invert the rule the registry was built on. The agent still
// picks WHERE from a list the destinations handed it, and PublishDocument still
// refuses a target that was not in that list; an agent-backed destination is a
// third implementation of the same interface, not a second way to choose. What
// changes is only what Publish() does once the choice is made.
//
// The seam is a registered closure for the same reason the standing runner and
// the channel runner are: this package must not depend on the package that owns
// the agent loop, and the app that configures a destination is not agent-aware
// either.

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// AgentPublisherFunc runs one agent against one instruction and returns what it
// reported doing. Registered by the agent-aware package (orchestrate).
//
// The reply is TEXT, not a structured result, because the whole point of this
// destination kind is that the framework does not know what the agent is going
// to do — file a ticket, open a pull request, hand it to a person. What it
// reports is what there is to record.
type AgentPublisherFunc func(ctx context.Context, user, agent, instruction string) (string, error)

var (
	agentPublisherMu sync.RWMutex
	agentPublisher   AgentPublisherFunc
)

// RegisterAgentPublisher installs the agent-execution closure. Call once at
// startup from the package that owns the agent loop.
func RegisterAgentPublisher(fn AgentPublisherFunc) {
	agentPublisherMu.Lock()
	agentPublisher = fn
	agentPublisherMu.Unlock()
}

// AgentPublisherReady reports whether an agent runner is installed, so a
// destination can say "this deployment cannot reach an agent" as a reason
// rather than failing at the moment somebody tries to publish.
func AgentPublisherReady() bool {
	agentPublisherMu.RLock()
	defer agentPublisherMu.RUnlock()
	return agentPublisher != nil
}

// ErrNoAgentPublisher is returned when a publish is routed to an agent in a
// deployment that has no agent runner installed.
var ErrNoAgentPublisher = errors.New("this deployment cannot hand a document to an agent")

// PublishViaAgent asks the named agent to carry out one instruction.
func PublishViaAgent(ctx context.Context, user, agent, instruction string) (string, error) {
	agentPublisherMu.RLock()
	fn := agentPublisher
	agentPublisherMu.RUnlock()
	if fn == nil {
		return "", ErrNoAgentPublisher
	}
	if strings.TrimSpace(agent) == "" {
		return "", errors.New("no agent is named for this destination")
	}
	return fn(ctx, user, agent, instruction)
}
