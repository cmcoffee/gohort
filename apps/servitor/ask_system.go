// The question half of the agent surface: handing an appliance question to
// servitor's own investigator instead of to the asking agent.
//
// request_capability covers "I need to DO something here, lastingly" —
// owner-authored, owner-approved, action-shaped. What it cannot cover is
// "what's the load right now?", because an open question has no fixed command
// to approve in advance. The wrong answer to that gap is giving the calling
// agent a shell; the right one is already in the building: the per-appliance
// investigator has the box mapped (facts, techniques, knowledge docs) and a
// worker with SSH — so the question ROUTES to it, and only the ANSWER comes
// back.
//
// The bridge is InvestigateSync, which is strictly read-only: every command
// the classifier calls destructive is auto-denied before anyone is asked. So
// connecting an agent (the same grant that lights request_capability) is a
// safe consent for this too — the caller gains "may ask about this machine",
// never "may change it".
package servitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// askSystemTimeout bounds one investigation. The lead+worker loop is tens of
// seconds on a healthy box; a wedged SSH session must not hold the calling
// agent's turn open forever.
const askSystemTimeout = 5 * time.Minute

// AskSystemToolDef is the question tool offered beside request_capability.
// connected is the provider's sorted appliance list, named in the description
// for the same reason request_capability names it: an agent that cannot see
// its reach answers "no I can't" to a machine it holds.
func AskSystemToolDef(udb Database, owner, agentID string, connected []Appliance) AgentToolDef {
	// "Servitor" appears by name because that is the word the OWNER uses. The
	// description described the capability perfectly and never named the thing
	// it belonged to, so an agent holding this tool answered "I have no
	// Servitor tools" when asked to consult Servitor — true of the vocabulary
	// it had been given, and wrong about what it could do.
	desc := "Ask Servitor a question about the live state or configuration of one of the owner's systems — \"what's the current load?\", \"is nginx running?\", \"how full is the data disk?\". " +
		"This is THE way to answer anything about the owner's machines, servers, appliances, lab systems or infrastructure: a Servitor investigator that already knows the system answers it, consulting what it has recorded and probing over SSH where records aren't enough. " +
		"Strictly read-only: it can inspect, never change. " +
		"Slow (tens of seconds) — ask one well-formed question rather than many small ones. " +
		"For ACTIONS (restart, deploy, clean up), use request_capability or an approved tool instead."
	if list := connectedSystemList(connected); list != "" {
		desc += " Systems you may ask Servitor about: " + list + "."
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "ask_system",
			Description: desc,
			Parameters: map[string]ToolParam{
				"system":   {Type: "string", Description: "Which machine, by the exact name or id from the connected-systems list in this tool's description."},
				"question": {Type: "string", Description: "What you want to know about it, in plain words. Include what you'll do with the answer if it helps focus the probe."},
			},
			Required: []string{"system", "question"},
			Caps:     []Capability{CapRead, CapExecute},
		},
		Handler: func(args map[string]any) (string, error) {
			system := StringArg(args, "system")
			question := StringArg(args, "question")
			appliance, ok := findAppliance(udb, system)
			if !ok {
				return "", fmt.Errorf("no system called %q — use one of the names in this tool's description exactly", system)
			}
			if !applianceEnabledForAgent(udb, agentID, appliance.ID) {
				return "", fmt.Errorf("you are not enabled for %s. Tell the person the owner has to connect you to that system in Servitor before you can ask about it",
					applianceLabel(appliance.Name, appliance.ID))
			}
			if servitorRef == nil {
				return "", fmt.Errorf("servitor is not running — the investigator cannot be reached")
			}
			// The investigation runs in the CONSOLE posture, exactly as the
			// Guides co-author does: no acting-agent stamp, because stamping
			// would gate the read-only probes on the agent's (usually empty)
			// auto-run grant and the auto-deny would starve the run. Safety
			// comes from InvestigateSync's own destructive-command denial, not
			// from the grant ladder. Logged so "who asked" survives anyway.
			Log("[servitor] agent %q asking %s: %s", agentID, appliance.ID, question)
			ctx, cancel := context.WithTimeout(context.Background(), askSystemTimeout)
			defer cancel()
			answer, err := servitorRef.InvestigateSync(ctx, owner, appliance.ID, question)
			if err != nil {
				return "", fmt.Errorf("the investigator could not answer: %w", err)
			}
			return answer, nil
		},
	}
}

// connectedSystemList renders the connected appliances for a tool description,
// annotating what each one IS.
//
// Two things an agent could not previously work out from a bare list of names.
//
// First, a WORKSPACE is not a machine. It reads exactly like one — a name in a
// list of "systems" — while asking it actually fans the question across every
// member. An agent front-ending a workspace has no way to know that the single
// entry it can see is the thing that reaches all the others, so it answers
// questions about the estate one machine at a time, or says it cannot.
//
// Second, the kind tells it what to expect. Asking a bundle of uploaded logs and
// asking a live SSH host are different acts with different latencies, and
// nothing in a name distinguishes them.
func connectedSystemList(connected []Appliance) string {
	if len(connected) == 0 {
		return ""
	}
	parts := make([]string, 0, len(connected))
	for _, a := range connected {
		label := applianceLabel(a.Name, a.ID)
		if k := applianceKindNote(a); k != "" {
			label += " (" + k + ")"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; ")
}

// applianceKindNote describes a type in the terms a CALLER needs, not the terms
// the record uses. An agent does not care that the type string is "workspace";
// it cares that one question reaches every member.
func applianceKindNote(a Appliance) string {
	switch a.Type {
	case "workspace":
		return "a GROUP of systems — one question here is answered across all of its members, so prefer it over asking each machine separately"
	case "repo":
		return "a source repository, not a live host — answers come from the code"
	case "bundle":
		return "uploaded evidence (logs, dumps) — a snapshot, nothing live to probe"
	case "toolset":
		return "reached through curated tools rather than a shell"
	case "command":
		return "a local command target"
	}
	return ""
}
