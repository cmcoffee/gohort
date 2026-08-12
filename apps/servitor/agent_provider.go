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
	all := map[string]Appliance{}
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, k, &a) {
			all[strings.ToLower(a.ID)] = a
		}
	}
	var enabled []Appliance
	granted := map[string]bool{}
	// member id -> the workspace label that reaches it, for the description.
	viaWorkspace := map[string]string{}
	for _, a := range all {
		if applianceEnabledForAgent(udb, agentID, a.ID) {
			enabled = append(enabled, a)
			granted[strings.ToLower(a.ID)] = true
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	// A workspace exists to be a handle for the machines inside it, so a
	// connection to one reaches its members for QUESTIONS. Without this an
	// agent granted an estate could ask about "the estate" and was refused on
	// every machine in it by name — the general question succeeding and the
	// obvious follow-up ("and what about lab-box specifically?") failing.
	//
	// The workspace itself stays askable and stays the right thing to ask for
	// anything estate-wide; the members are for when a question is about one
	// machine.
	//
	// Kept as a SEPARATE list from the connected one, because the two tools
	// honor different rules: request_capability creates an ability on a machine
	// and keys on a direct connection, while ask_system reads and keys on this.
	// Handing both the same list would put a machine in one tool's description
	// that the same tool then refuses by name.
	askable := append([]Appliance{}, enabled...)
	for _, a := range enabled {
		if a.Type != "workspace" {
			continue
		}
		for _, id := range a.Members {
			key := strings.ToLower(strings.TrimSpace(id))
			m, ok := all[key]
			if !ok || granted[key] {
				continue // unknown, or already connected in its own right
			}
			granted[key] = true
			viaWorkspace[key] = applianceLabel(a.Name, a.ID)
			askable = append(askable, m)
		}
	}
	sortAppliancesByID(askable)
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
		// than being something the calling agent needs a shell for. Gets the
		// WIDE list — workspace members included — matching what its handler
		// accepts.
		AskSystemToolDef(udb, owner, agentID, askable, viaWorkspace),
	}
	for _, a := range enabled {
		// Only a machine the agent is connected to IN ITS OWN RIGHT contributes
		// approved command tools. Reached-via-workspace members are askable and
		// nothing more — see the membership expansion above.
		if !applianceEnabledForAgent(udb, agentID, a.ID) {
			continue
		}
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
