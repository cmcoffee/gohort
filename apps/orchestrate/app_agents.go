// App Agents — orchestrate's adapter for the cross-app agent registry
// (core/app_agents.go). Apps register an AppAgentSpec in their init(); here we
// map each spec to an AgentRecord and fold it into seedAgents(), so a
// registered app agent resolves through loadAgent (framework/app owns the
// prompt, the per-user shadow overlay owns operational state), lists in the
// Agency dashboard, and is dispatchable — all via the SAME machinery as
// orchestrate's own seeds, with no new resolution path.
//
// Next step (not in this slice): a dedicated "App Agents" dashboard section
// grouped by OwningApp. listAgents already surfaces these; AppAgentByID lets
// that future section tell app-owned agents apart from seeds + user records.

package orchestrate

import (
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// isAppAgent reports whether id belongs to a code-registered App Agent. This is
// the AUTHORITATIVE app-agent test — it keys on the registry, not on drift-able
// runtime proxies (Owner=="system", Hidden), which a stale per-user shadow can
// forge. Every safeguard that must hold the app-agent boundary (no LLM-authored
// tools, not a scope target) keys on THIS, so the boundary can't be walked
// across by a mis-saved record. (Phase 0 of the AgentRecord split — until the
// type itself carries the boundary, this is the single source of truth.)
func isAppAgent(id string) bool {
	_, ok := appagents.AppAgentByID(id)
	return ok
}

// appAgentVisibilityWarnOnce fires the visible-app-agent lint a single time,
// the first time app agents are folded into resolution (startup / first agent
// list). Guarded so the per-request registeredAppAgents() call doesn't spam.
var appAgentVisibilityWarnOnce sync.Once

// warnVisibleAppAgents logs a one-time WARNING for every app-agent registered
// NON-Hidden. A visible app-agent shows in the picker AND becomes a tool-scope
// target, so a stray tool can be mis-scoped onto it (the Casefile "Case
// Analyzer" incident). The removal + Hidden-from-spec fixes make that
// recoverable, but the trap is easy to walk into — RegisterAppAgent can't lint
// (it's a stdlib-only leaf), so the nudge lives here where nfo logging exists.
func warnVisibleAppAgents() {
	for _, s := range appagents.AppAgents() {
		if !s.Hidden {
			Warn("[app-agents] %q (%s) is registered VISIBLE (Hidden:false) — it appears in the agent picker and becomes a tool-scope target, so a stray tool can be mis-scoped onto it. Register it Hidden:true unless users are meant to chat with it directly.", s.Name, s.ID)
		}
	}
}

// appAgentSpecToRecord maps a portable appagents.AppAgentSpec onto orchestrate's
// AgentRecord. Owner=seedOwner marks it framework/app-owned (read-mostly, with
// a per-user shadow for customization), matching how seeds are tagged.
func appAgentSpecToRecord(s appagents.AppAgentSpec) AgentRecord {
	return AgentRecord{
		ID:                 s.ID,
		Owner:              seedOwner,
		Name:               s.Name,
		Description:        s.Description,
		OrchestratorPrompt: s.Prompt,
		AllowedTools:       s.AllowedTools,
		Hidden:             s.Hidden,
		Cortex:             s.Cortex,
		MemoryMode:         s.MemoryMode,
		DisableExplicit:    s.DisableExplicit,
	}
}

// registeredAppAgents converts every cross-app registered agent to a record.
func registeredAppAgents() []AgentRecord {
	appAgentVisibilityWarnOnce.Do(warnVisibleAppAgents)
	specs := appagents.AppAgents()
	out := make([]AgentRecord, 0, len(specs))
	for _, s := range specs {
		out = append(out, appAgentSpecToRecord(s))
	}
	return out
}

// seedAgents returns orchestrate's own in-code seeds plus every app-registered
// agent. seedAgentByID / isSeedID / loadAgent's miss path / listAgents pass 2
// all walk this, so app agents inherit seed resolution + shadow overlay for
// free. coreSeedAgents() returns a fresh slice each call, so the append never
// aliases the in-code literal.
func seedAgents() []AgentRecord {
	return append(coreSeedAgents(), registeredAppAgents()...)
}
