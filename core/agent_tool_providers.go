// Letting an app put tools in an agent's hands.
//
// Some capabilities belong to an app rather than to the framework, and which
// agent gets them depends on a binding only that app maintains — servitor's
// per-machine grants, and whatever the next one turns out to be. The app cannot
// hand them over directly: it sits ABOVE the agent runtime in the import graph
// (servitor imports orchestrate), so the runtime cannot reach down and ask.
//
// So the app pushes and the runtime pulls, the same shape as every other seam
// here (RegisterChannelAgentRunner, RegisterScopedToolLister). The runtime knows
// nothing about machines or grants; it knows that providers exist and asks them
// all the same question: given this agent, what may it do?
//
// THREE PROPERTIES THIS GUARANTEES, because all three have bitten elsewhere:
//
// Order is stable. Providers are asked in name order and their tools keep the
// order they were returned in, so the catalog is byte-identical between turns
// and the prompt prefix caches. A map walk here would reshuffle the tool list
// on every request for no reason.
//
// A broken provider costs its own tools and nothing else. It runs behind a
// recover, because an app panicking while assembling a catalog would otherwise
// take down a turn that had nothing to do with it.
//
// Nothing is implicit. A provider returning tools for an agent is that app
// asserting the agent is entitled to them; this file does not second-guess it,
// and equally does not grant anything on its own.
package core

import (
	"sort"
	"sync"
)

// AgentToolProvider returns the tools an app is contributing for one agent.
// Return nil when the agent has none — that is the common case, and it must be
// cheap.
//
// sess is the run's session, so a provider can bind handlers to it. owner is
// whose fleet the agent belongs to; agentID is the agent itself.
type AgentToolProvider func(sess *ToolSession, owner, agentID string) []AgentToolDef

var (
	agentToolProviderMu sync.RWMutex
	agentToolProviders  = map[string]AgentToolProvider{}
)

// RegisterAgentToolProvider installs a provider under a name. Call once at
// startup, from the app that owns the binding.
//
// A repeat registration REPLACES and says so. Silently keeping either one hides
// a real mistake — two apps claiming the same name, or an init running twice —
// and the resulting catalog would be whichever the map iteration favoured.
func RegisterAgentToolProvider(name string, fn AgentToolProvider) {
	if name == "" || fn == nil {
		return
	}
	agentToolProviderMu.Lock()
	defer agentToolProviderMu.Unlock()
	if _, dup := agentToolProviders[name]; dup {
		Log("[tools] agent tool provider %q registered twice — replacing the earlier one", name)
	}
	agentToolProviders[name] = fn
}

// AgentProvidedTools collects every app-contributed tool for one agent.
//
// Returns nil when nothing is contributed, so callers can append
// unconditionally without a length check changing the catalog.
func AgentProvidedTools(sess *ToolSession, owner, agentID string) []AgentToolDef {
	if agentID == "" {
		return nil
	}
	agentToolProviderMu.RLock()
	names := make([]string, 0, len(agentToolProviders))
	for name := range agentToolProviders {
		names = append(names, name)
	}
	snapshot := make(map[string]AgentToolProvider, len(agentToolProviders))
	for name, fn := range agentToolProviders {
		snapshot[name] = fn
	}
	agentToolProviderMu.RUnlock()

	sort.Strings(names)
	var out []AgentToolDef
	for _, name := range names {
		out = append(out, safeProviderTools(name, snapshot[name], sess, owner, agentID)...)
	}
	return out
}

// safeProviderTools runs one provider behind a recover. A provider that panics
// contributes nothing and the turn continues — with a line naming it, because a
// capability that silently stopped appearing is the hardest kind to notice.
func safeProviderTools(name string, fn AgentToolProvider, sess *ToolSession, owner, agentID string) (defs []AgentToolDef) {
	defer func() {
		if r := recover(); r != nil {
			Log("[tools] agent tool provider %q panicked (%v) — contributing nothing this turn", name, r)
			defs = nil
		}
	}()
	return fn(sess, owner, agentID)
}
