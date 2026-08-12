// Servitor's answer to "what may this agent do?".
//
// Registered once at startup; the agent runtime asks every provider the same
// question on every dispatch and never learns what a machine is. What comes
// back is decided entirely here, from records the owner maintains.
//
// THREE THINGS ARE OFFERED, and the split matters:
//
//	request_capability   — to an agent connected to ANY machine
//	ask_system           — same gate: questions routed to the investigator
//	the minted tools     — per machine, and only the approved ones
//
// An agent with a connection but no approved tools still gets the asking
// tools, because otherwise it has no way to answer a question about the box or
// to tell anyone what it needs, and the loop never starts. An agent connected
// to nothing gets none of it, so a fleet where nobody has enabled anything
// pays no tokens for a capability it cannot use.
package servitor

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterAgentToolProvider("servitor.appliances", applianceToolProvider)
	// The visibility half: the tool provider says what an agent CAN DO, this
	// says what it HOLDS, in the place someone looks when asking about an agent
	// rather than about a machine.
	RegisterAgentGrantor(AgentGrantor{
		Name: "servitor", Label: "Machines", ManageURL: "/servitor/manage",
		Granted: applianceGrantsFor,
	})
}

// applianceGrantsFor lists the machines an agent is connected to, with what
// runs there without asking.
//
// Reports what is HELD and never a default — a grant record is the only thing
// that produces a row. The exec path still falls back to the owner's own
// auto-run settings when no grant names an agent, which is right for the
// console and is exactly the kind of implicit permission this display must not
// report as though somebody had chosen it.
func applianceGrantsFor(user, agentID string) []AgentGrant {
	udb := servitorUserDB(user)
	if udb == nil || agentID == "" {
		return nil
	}
	names := map[string]string{}
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, k, &a) {
			names[strings.ToLower(a.ID)] = applianceLabel(a.Name, a.ID)
		}
	}
	var out []AgentGrant
	for _, g := range ListCommandGrants(udb) {
		if !strings.EqualFold(g.AgentID, normalizeAgentID(agentID)) {
			continue
		}
		label := names[strings.ToLower(g.ApplianceID)]
		if label == "" {
			label = g.ApplianceID + " (removed)"
		}
		out = append(out, AgentGrant{Label: label, Detail: allowsText(g.Categories)})
	}
	return out
}

// applianceToolProvider builds the catalog for one agent.
//
// Reads the owner's store rather than the runtime user's. A channel run acts as
// a synthetic per-chat identity, so asking about THAT user would find no
// machines, no grants, and quietly hand back an empty catalog — the same
// wrong-store bug that made an agent invisible to list_agents earlier.
func applianceToolProvider(sess *ToolSession, owner, agentID string) []AgentToolDef {
	udb := servitorUserDB(owner)
	if udb == nil || agentID == "" {
		return nil
	}
	var enabled []Appliance
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if !udb.Get(applianceTable, k, &a) {
			continue
		}
		if applianceEnabledForAgent(udb, agentID, a.ID) {
			enabled = append(enabled, a)
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	// Deterministic: the tool list is part of the prompt, and a catalog that
	// reshuffles between turns costs a prefix-cache miss for nothing.
	sortAppliancesByID(enabled)

	var chat FactChatFunc
	if servitorRef != nil {
		chat = servitorRef.WorkerChat
	}
	out := []AgentToolDef{
		RequestCapabilityToolDef(udb, chat, agentID, enabled),
		// The question route: open-ended "what's the state of X?" goes to the
		// per-appliance investigator (read-only, via InvestigateSync) rather
		// than being something the calling agent needs a shell for.
		AskSystemToolDef(udb, owner, agentID, enabled),
	}
	for _, a := range enabled {
		out = append(out, ApplianceToolDefs(udb, owner, agentID, a)...)
	}
	return out
}

func sortAppliancesByID(list []Appliance) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].ID < list[j-1].ID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// servitorRef is the running instance, for the worker model the mint step
// needs. Set at startup; nil in tests and in a deployment where servitor is
// compiled in but never started, where request_capability degrades to an
// honest "no model available" rather than a crash.
var servitorRef *Servitor

// RegisterServitorInstance records the running app so the provider can reach
// its worker model.
func RegisterServitorInstance(s *Servitor) { servitorRef = s }
