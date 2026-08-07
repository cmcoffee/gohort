// The agent-facing half: asking for a capability it does not have.
//
// An agent that needs to do something on a machine has two possible shapes of
// answer. It can be handed a general shell and told to be careful, or it can
// ask for the specific thing and be given a tool that does exactly that. This
// is the second: the agent describes the JOB, servitor works out the command
// against what it knows about the box, and the result is a proposal the owner
// reads and approves.
//
// The point of the round trip is that the model is never the author of a
// command that runs. It supplies an intent — prose, not syntax — and receives a
// tool whose structure was fixed by something else and agreed by someone else.
//
// TWO THINGS THIS DELIBERATELY REFUSES:
//
// An appliance the agent has not been enabled for. Requesting is cheap, and
// without a scope any agent could enumerate every machine the owner owns by
// asking for capabilities on each and reading the error.
//
// Overwriting a tool that is already APPROVED. Same command name, new template,
// and the old approval would carry the new command — the laundering shape that
// keeps recurring: re-keeping an image under a new name, a told claim retiring
// a checked one. An approved tool is replaced only by the owner deleting it.
package servitor

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// applianceEnabledForAgent reports whether the owner has connected this agent
// to this machine at all.
//
// The grant record IS the enable switch, rather than a second flag beside it. A
// grant naming (agent, appliance) — even one permitting NOTHING to auto-run —
// says the owner has considered this pairing, which is the question being asked
// here. Absence means they have not.
//
// Console callers (no agent) are always enabled: that is the owner at their own
// keyboard, which is who the enable would be protecting.
func applianceEnabledForAgent(udb Database, agentID, applianceID string) bool {
	if strings.TrimSpace(agentID) == "" {
		return true
	}
	_, ok := loadCommandGrant(udb, agentID, applianceID)
	return ok
}

// findAppliance resolves a machine by id or name, case-insensitively. Name
// first is deliberate: an agent that read a list is quoting a NAME back, and
// failing that lookup would send it guessing at ids.
func findAppliance(udb Database, ref string) (Appliance, bool) {
	ref = strings.TrimSpace(ref)
	if udb == nil || ref == "" {
		return Appliance{}, false
	}
	var byID Appliance
	var haveID bool
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if !udb.Get(applianceTable, k, &a) {
			continue
		}
		if strings.EqualFold(a.Name, ref) {
			return a, true
		}
		if strings.EqualFold(a.ID, ref) {
			byID, haveID = a, true
		}
	}
	return byID, haveID
}

// RequestCapabilityArgs is one request for a new tool.
type RequestCapabilityArgs struct {
	System string // machine name or id
	Intent string // what the tool should do, in prose
}

// RequestCapability mints a proposal and stores it unapproved.
//
// Returns text written for the AGENT, because the agent is what reads it and
// then has to explain the situation to a person. It says what was proposed, the
// exact command, and — the part that matters — that nothing will happen until
// the owner approves.
func RequestCapability(ctx context.Context, udb Database, chat FactChatFunc, agentID string, in RequestCapabilityArgs) (string, error) {
	appliance, ok := findAppliance(udb, in.System)
	if !ok {
		return "", fmt.Errorf("no system called %q — list the systems first and use one of those names exactly", in.System)
	}
	if !applianceEnabledForAgent(udb, agentID, appliance.ID) {
		// Same wording whether the machine exists or not would be better for
		// secrecy, but worse for the person: this is the owner's own fleet and
		// the agent needs to be able to tell them what to switch on.
		return "", fmt.Errorf("you are not enabled for %s. Nothing was requested — tell the person that the owner has to connect you to that system in Servitor before you can ask for capabilities on it",
			applianceLabel(appliance.Name, appliance.ID))
	}
	tool, err := MintApplianceTool(ctx, chat, appliance, factsForAppliance(udb, appliance.ID), in.Intent, agentID)
	if err != nil {
		return "", err
	}
	if existing, found := LoadApplianceTool(udb, appliance.ID, tool.Name); found {
		if existing.Approved {
			return "", fmt.Errorf("%q already exists on %s and is approved, running: %s. It was NOT changed — an approved tool keeps the command the owner read. If this needs to do something different, ask for it under a different name, or have the owner delete the existing one first",
				existing.Name, applianceLabel(appliance.Name, appliance.ID), existing.Template)
		}
		// Replacing an unapproved proposal is fine — nobody has agreed to it.
		Log("[servitor] agent %q replaced its unapproved proposal %q on %s", agentID, tool.Name, appliance.ID)
	}
	saved, err := SaveApplianceTool(udb, tool)
	if err != nil {
		return "", fmt.Errorf("could not record that capability: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Requested %q for %s — WAITING FOR APPROVAL, nothing has run.\n\n", saved.Name, applianceLabel(appliance.Name, appliance.ID))
	fmt.Fprintf(&b, "It would run: %s\n", saved.Template)
	if saved.Risk != RiskNone {
		fmt.Fprintf(&b, "Classified as: %s\n", string(saved.Risk))
	}
	if len(saved.Params) > 0 {
		fmt.Fprintf(&b, "Values you would supply: %s\n", strings.Join(sortedParamNames(saved.Params), ", "))
	}
	b.WriteString("\nTell the person what you asked for and that it needs their approval in Servitor. " +
		"Do NOT wait for it, do NOT ask again, and do NOT try to achieve the same thing another way — if they approve it, you will simply have the tool next time.")
	return b.String(), nil
}

func sortedParamNames(p map[string]ToolParam) []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	// Sorted so the same tool reads the same way twice.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// RequestCapabilityToolDef is the tool an agent is offered. Read-capability
// only: it writes a PROPOSAL, and a proposal cannot do anything.
func RequestCapabilityToolDef(udb Database, chat FactChatFunc, agentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "request_capability",
			Description: "Ask for a new ability on one of the owner's systems, described in plain words — \"restart the web server\", \"tail the app log\", \"deploy a given version\". " +
				"Servitor works out the exact command for THAT machine and stores it as a proposal for the owner to approve. " +
				"Nothing runs now, and nothing runs later without their approval. " +
				"Use this when you need to do something on a system and have no tool for it; do not use it to run something once — it exists to create a lasting, named ability. " +
				"After calling it, tell the person what you asked for and move on.",
			Parameters: map[string]ToolParam{
				"system": {Type: "string", Description: "Which machine, by the name or id shown when systems are listed."},
				"intent": {Type: "string", Description: "What the tool should DO, in prose. Describe the job, not a command — \"restart the web server\", not \"sudo systemctl restart nginx\". Say what varies between runs (a version, a filename, a service) so it becomes a value you can supply each time."},
			},
			Required: []string{"system", "intent"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			return RequestCapability(context.Background(), udb, chat, agentID, RequestCapabilityArgs{
				System: StringArg(args, "system"),
				Intent: StringArg(args, "intent"),
			})
		},
	}
}
