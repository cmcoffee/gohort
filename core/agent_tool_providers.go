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
		Log("[tools] agent tool provider %q registered twice: replacing the earlier one", name)
	}
	agentToolProviders[name] = fn
}

// AgentProvidedTools collects every app-contributed tool for one agent.
//
// Returns nil when nothing is contributed, so callers can append
// unconditionally without a length check changing the catalog.
func AgentProvidedTools(sess *ToolSession, owner, agentID string) []AgentToolDef {
	var out []AgentToolDef
	walkAgentToolProviders(sess, owner, agentID, func(_ string, defs []AgentToolDef) {
		out = append(out, defs...)
	})
	return out
}

// AgentProvidedToolOrigins maps each contributed tool NAME to the app that
// contributed it.
//
// For a surface that has to say what a tool REACHES. An app-provided tool
// arrives looking exactly like a framework one - it is in the same catalog,
// built the same way - but the app is there because the capability belongs to
// a system the owner connected, so "read_file" and "a shell on an appliance"
// were presented identically on the page built for deciding which to allow.
//
// Attribution by PROVENANCE, not by anything the tool says about itself. The
// name a tool claims and the category it claims are both editable by whoever
// wrote it; which registry handed it over is not, and on a security surface
// that is the difference between a fact and a suggestion.
func AgentProvidedToolOrigins(sess *ToolSession, owner, agentID string) map[string]string {
	out := map[string]string{}
	walkAgentToolProviders(sess, owner, agentID, func(provider string, defs []AgentToolDef) {
		for _, d := range defs {
			if n := d.Tool.Name; n != "" {
				out[n] = provider
			}
		}
	})
	return out
}

// walkAgentToolProviders asks every provider in name order and hands each
// one's tools to visit. The order guarantee in this file's header lives here,
// so the two callers above cannot drift on it.
func walkAgentToolProviders(sess *ToolSession, owner, agentID string, visit func(provider string, defs []AgentToolDef)) {
	if agentID == "" {
		return
	}
	agentToolProviderMu.RLock()
	names := make([]string, 0, len(agentToolProviders))
	snapshot := make(map[string]AgentToolProvider, len(agentToolProviders))
	for name, fn := range agentToolProviders {
		names = append(names, name)
		snapshot[name] = fn
	}
	agentToolProviderMu.RUnlock()

	sort.Strings(names)
	for _, name := range names {
		if defs := safeProviderTools(name, snapshot[name], sess, owner, agentID); len(defs) > 0 {
			visit(name, defs)
		}
	}
}

// safeProviderTools runs one provider behind a recover. A provider that panics
// contributes nothing and the turn continues — with a line naming it, because a
// capability that silently stopped appearing is the hardest kind to notice.
func safeProviderTools(name string, fn AgentToolProvider, sess *ToolSession, owner, agentID string) (defs []AgentToolDef) {
	defer func() {
		if r := recover(); r != nil {
			Log("[tools] agent tool provider %q panicked (%v): contributing nothing this turn", name, r)
			defs = nil
		}
	}()
	return fn(sess, owner, agentID)
}
