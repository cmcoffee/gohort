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
	names := make([]string, 0, len(connected))
	for _, a := range connected {
		names = append(names, applianceLabel(a.Name, a.ID))
	}
	desc := "Ask a question about the live state or configuration of one of the owner's systems — \"what's the current load?\", \"is nginx running?\", \"how full is the data disk?\". " +
		"A specialist investigator that knows the machine answers it, probing over SSH where records aren't enough. Strictly read-only: it can inspect, never change. " +
		"Slow (tens of seconds) — ask one well-formed question rather than many small ones. " +
		"For ACTIONS (restart, deploy, clean up), use request_capability or an approved tool instead."
	if len(names) > 0 {
		desc += " Systems you may ask about: " + strings.Join(names, "; ") + "."
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
