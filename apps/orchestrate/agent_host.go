package orchestrate

// The agent host other apps reach through core/agenthost (registered in
// registerExternalAgentHooks). Most of agenthost.Host is methods this app
// already had; these are the few that existed only as package functions, and
// the conversion between an AgentRecord and the agenthost.Agent an app holds.

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

var _ agenthost.Host = (*OrchestrateApp)(nil)

// AgentNames maps each agent visible to user, by id, to its name: their own
// agents and the seeds they have not replaced (AgentsForUser).
func (T *OrchestrateApp) AgentNames(user string) map[string]string {
	out := map[string]string{}
	for _, a := range T.AgentsForUser(user) {
		out[a.ID] = a.Name
	}
	return out
}

// MachineStore is the store owner's machines are kept in.
func (T *OrchestrateApp) MachineStore(owner string) Database {
	return PublicMachineStore(owner)
}

// AgentStateNamespace is the namespace agentID's facts and memory are kept
// under (the package function of the same name).
func (T *OrchestrateApp) AgentStateNamespace(agentID string) string {
	return AgentStateNamespace(agentID)
}

// AppAgentOf is rec as an app holds it for a turn.
func AppAgentOf(rec AgentRecord) agenthost.Agent {
	return agenthost.Agent{
		ID:                     rec.ID,
		Name:                   rec.Name,
		OrchestratorPrompt:     rec.OrchestratorPrompt,
		AllowedTools:           rec.AllowedTools,
		DispatchMode:           rec.DispatchMode,
		AllowedDispatchTargets: rec.AllowedDispatchTargets,
		ForcePrivate:           rec.ForcePrivate,
		EnableNotes:            rec.EnableNotes,
		SeedNotes:              rec.SeedNotes,
		LeadModel:              rec.LeadModel,
		ThinkBudget:            rec.ThinkBudget,
		Record:                 rec,
	}
}

// appRecord is the record behind a, with what the app set for its turn
// written onto it.
func appRecord(a agenthost.Agent) AgentRecord {
	rec, _ := a.Record.(AgentRecord)
	rec.ID = a.ID
	rec.Name = a.Name
	rec.OrchestratorPrompt = a.OrchestratorPrompt
	rec.AllowedTools = a.AllowedTools
	rec.DispatchMode = a.DispatchMode
	rec.AllowedDispatchTargets = a.AllowedDispatchTargets
	rec.ForcePrivate = a.ForcePrivate
	rec.EnableNotes = a.EnableNotes
	rec.SeedNotes = a.SeedNotes
	rec.LeadModel = a.LeadModel
	rec.ThinkBudget = a.ThinkBudget
	return rec
}
