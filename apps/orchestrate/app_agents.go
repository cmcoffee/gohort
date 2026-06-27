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
	. "github.com/cmcoffee/gohort/core"
)

// appAgentSpecToRecord maps a portable core.AppAgentSpec onto orchestrate's
// AgentRecord. Owner=seedOwner marks it framework/app-owned (read-mostly, with
// a per-user shadow for customization), matching how seeds are tagged.
func appAgentSpecToRecord(s AppAgentSpec) AgentRecord {
	return AgentRecord{
		ID:                 s.ID,
		Owner:              seedOwner,
		Name:               s.Name,
		Description:        s.Description,
		OrchestratorPrompt: s.Prompt,
		AllowedTools:       s.AllowedTools,
		Hidden:             s.Hidden,
		Cortex:             s.Cortex,
	}
}

// registeredAppAgents converts every cross-app registered agent to a record.
func registeredAppAgents() []AgentRecord {
	specs := AppAgents()
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
