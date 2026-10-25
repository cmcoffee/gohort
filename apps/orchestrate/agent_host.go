package orchestrate

// The agent host other apps reach through core/agenthost (registered in
// orchestrate.go). Most of agenthost.Host is methods this app already had;
// these are the few that existed only as package functions or as a call an
// app had to make on a full AgentRecord.

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
