// Servitor's answer to "what may this agent do?".
//
// Registered once at startup; the agent runtime asks every provider the same
// question on every dispatch and never learns what a machine is. What comes
// back is decided entirely here, from records the owner maintains.
//
// THREE THINGS ARE OFFERED, and the split matters:
//
//	request_capability   — to an agent with ANY of the owner's systems in its Sources
//	ask_system           — questions for a member reached through a workspace in
//	                       its Sources; a system IN its Sources is asked through
//	                       that Source's own investigate_<system>, the same run
//	the minted tools     — per system, and only the approved ones
//
// "In its Sources" is the door (applianceEnabledForAgent): chosen on the
// agent's own page, so what an agent can reach is decided and seen there. A
// command grant only says what runs without asking.
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
	//
	// ManageURL is the app's own page: access is edited from the Access
	// button beside the appliance there. It pointed at "/servitor/manage",
	// a page removed when access moved onto the appliance, so the link 404'd.
	RegisterAgentGrantor(AgentGrantor{
		Name: "servitor", Label: "Servitor systems", ManageURL: servitorManageURL,
		Granted: applianceGrantsFor,
	})
}

// servitorManageURL is where an agent's machine access is changed. Kept equal
// to WebPath (see TestGrantorManageURLIsServed).
const servitorManageURL = "/servitor"

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
		if applianceEnabledForAgent(udb, owner, agentID, a.ID) {
			// Chosen as a Source with no grant yet: it gets one that lets
			// nothing run without asking, the state connecting has always
			// written. Without it the exec gate would fall back to the
			// owner's own console settings, and adding a Source would hand
			// an agent more than connecting it ever did.
			if _, has := loadCommandGrant(udb, agentID, a.ID); !has {
				SaveCommandGrant(udb, agentID, a.ID, nil)
				Log("[servitor] agent %q has %s as a Source: recorded with nothing running without asking", normalizeAgentID(agentID), a.ID)
			}
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
	// A system in the agent's Sources is asked through its own
	// investigate_<system> (servitorSource), the same investigation this
	// tool runs. Listing it here too gave the agent two tools for one
	// question; ask_system now covers only what nothing else does: a member
	// reached through a workspace in its Sources.
	var askable []Appliance
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
			if m.ToolsOnly {
				// Reaching a target through a workspace must not grant
				// what a direct connection would have withheld, or the
				// flag is bypassed by putting the target in a group.
				continue
			}
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
		RequestCapabilityToolDef(sess, udb, chat, owner, agentID, enabled),
	}
	// The question route: open-ended "what's the state of X?" goes to the
	// per-appliance investigator (read-only, via InvestigateSync) rather
	// than being something the calling agent needs a shell for. Gets the
	// WIDE list — workspace members included — matching what its handler
	// accepts.
	//
	// Omitted entirely when every reachable target is ToolsOnly: a tool
	// whose only honest answer is "there is nothing you may ask about"
	// teaches the model to stop reading its own catalog.
	if len(askable) > 0 {
		out = append(out, AskSystemToolDef(sess, udb, owner, agentID, askable, viaWorkspace))
	}
	for _, a := range enabled {
		// Only a machine the agent is connected to IN ITS OWN RIGHT contributes
		// approved command tools. Reached-via-workspace members are askable and
		// nothing more — see the membership expansion above.
		if !applianceEnabledForAgent(udb, owner, agentID, a.ID) {
			continue
		}
		out = append(out, ApplianceToolDefs(sess, udb, owner, agentID, a)...)
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
