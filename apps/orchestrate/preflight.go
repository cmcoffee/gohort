// Unattended-run pre-flight — answers "what would be refused if this fired right
// now" at the moment a schedule is AUTHORED, instead of hours later when it runs.
//
// Two gates decide an unattended fire, and neither is consulted until the fire
// happens:
//
//   - The TOOL gate (autonomousGate.confirm): a NeedsConfirm tool runs only if
//     the owner pre-authorized it in AutoApproveTools. Otherwise it is refused
//     for that fire and queued as an "autonomous_tool" authorization.
//   - The RECIPIENT gate (the send_message action in channel_tools.go): a
//     proactive message goes out only if the contact is pre-authorized or the
//     agent is an authorized sender for the channel. Otherwise it is queued as a
//     "send_message" authorization.
//
// Both are trigger-dependent, which is the whole friction: an INTERACTIVE
// dispatch auto-confirms every tool (RunAgentSyncContinuing passes a Confirm
// that always returns true) and can reply in-thread to skip the recipient gate
// entirely (isReplyToActiveInbound). A scheduled fire has neither — no human to
// confirm, and no active inbound to reply to. So the identical agent, tools, and
// prompt behave one way when a person asks and another way on a timer, and the
// difference only ever showed up after the fact.
//
// This does NOT change either policy. It runs the same predicates early so the
// gap is visible while the author can still act on it.
package orchestrate

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Pre-flight gate kinds.
const (
	PreflightGateTool      = "tool"
	PreflightGateRecipient = "recipient"
)

// PreflightFinding is one thing that would be refused on an unattended fire.
type PreflightFinding struct {
	Gate   string `json:"gate"`            // PreflightGateTool | PreflightGateRecipient
	Name   string `json:"name"`            // tool name, or recipient label
	Detail string `json:"detail"`          // what happens, in the author's terms
	Fix    string `json:"fix,omitempty"`   // the one action that clears it
	Fatal  bool   `json:"fatal,omitempty"` // refused outright rather than queued for approval
}

// PreflightAutonomous predicts what an unattended run of this agent would be
// refused, without running anything. An empty result means a fire would not hit
// either gate.
//
// Deliberately advisory: it reports, it does not block. A schedule whose whole
// point is to surface an approval request is legitimate, and so is one authored
// before its tools are granted. The cost of being wrong here is a warning the
// author dismisses; blocking would make it a wall.
func (app *OrchestrateApp) PreflightAutonomous(owner, agentID string) []PreflightFinding {
	owner, agentID = strings.TrimSpace(owner), strings.TrimSpace(agentID)
	if owner == "" || agentID == "" {
		return nil
	}
	udb := UserDB(app.DB, owner)
	agent, ok := loadAgent(udb, agentID)
	if !ok {
		return nil
	}
	return append(preflightTools(udb, agent), preflightRecipients(udb, owner, agent)...)
}

// confirmingTool is one of the agent's tools reduced to what the gate cares
// about. Resolution (which needs the live registry) is kept out of the policy
// below so the rule itself is testable on its own.
type confirmingTool struct {
	Name         string
	NeedsConfirm bool
}

// preflightTools resolves the agent's tools and applies the gate rule.
func preflightTools(udb Database, agent AgentRecord) []PreflightFinding {
	// Resolve one at a time: a single unresolvable name (a custom tool that only
	// materializes with a live session) must not blank the whole report.
	var tools []confirmingTool
	for _, name := range agent.AllowedTools {
		defs, err := GetAgentTools(name)
		if err != nil || len(defs) == 0 {
			continue // can't resolve it here; the fire will resolve it for real
		}
		tools = append(tools, confirmingTool{Name: name, NeedsConfirm: defs[0].NeedsConfirm})
	}
	return preflightToolFindings(agent, autonomousApprovedSet(udb, agent.ID), tools)
}

// preflightToolFindings mirrors autonomousGate.confirm: every NeedsConfirm tool
// must be in the inherited AutoApproveTools set, or that call is refused and
// queued when the task fires. Pure — same inputs the gate sees, same decision,
// just made early.
func preflightToolFindings(agent AgentRecord, approved map[string]bool, tools []confirmingTool) []PreflightFinding {
	// A sub-agent runs under its parent's authority — the gate returns true for
	// everything, so there is nothing to warn about. Mirrors newAutonomousGate's
	// subAgent flag; keep the two in step.
	if strings.TrimSpace(agent.OwnedBy) != "" {
		return nil
	}
	var out []PreflightFinding
	for _, tl := range tools {
		if !tl.NeedsConfirm || approved[tl.Name] {
			continue
		}
		out = append(out, PreflightFinding{
			Gate: PreflightGateTool,
			Name: tl.Name,
			Detail: fmt.Sprintf("%q asks for confirmation, and nobody is present on a scheduled run. "+
				"The call is refused for that fire and queued for approval instead.", tl.Name),
			Fix: fmt.Sprintf("Pre-authorize %q for this agent (Auto-approve tools), or approve it once from the Authorizations pane after the first fire.", tl.Name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// preflightRecipients mirrors the send_message gate for every channel this agent
// can reach. The in-thread bypass is deliberately NOT considered: a scheduled
// fire has no active inbound to reply to, so a send that works interactively can
// still queue on a timer — precisely the case this exists to surface.
func preflightRecipients(udb Database, owner string, agent AgentRecord) []PreflightFinding {
	var out []PreflightFinding
	for _, ch := range channelsAccessibleToAgent(udb, owner, agent.ID) {
		chatID := strings.TrimSpace(ch.Address)
		if chatID == "" {
			// Whole-service binding with no specific address — there's no single
			// recipient to test, and inbound-driven replies are in-thread anyway.
			continue
		}
		recip := operatorRecipientKey(chatID, "")
		label := chFirst(ch.Name, chatID)
		switch {
		case IsContactBlocked(RootDB, owner, recip):
			out = append(out, PreflightFinding{
				Gate: PreflightGateRecipient, Name: label, Fatal: true,
				Detail: fmt.Sprintf("%s is blocked in permission settings — a scheduled message is dropped, not queued.", label),
				Fix:    "Unblock the contact in permission settings if this schedule is meant to reach them.",
			})
		case IsContactPreAuthorized(RootDB, owner, recip):
		case channelSenderAuthorized(udb, owner, chatID, "", agent.ID):
		default:
			out = append(out, PreflightFinding{
				Gate: PreflightGateRecipient, Name: label,
				Detail: fmt.Sprintf("Messaging %s unprompted needs approval. Answering an incoming message is in-thread and sends freely, "+
					"but a scheduled fire has no incoming message to answer — so it queues instead of sending.", label),
				Fix: fmt.Sprintf("Pre-authorize the contact, or add this agent as an authorized sender on %q.", chFirst(ch.Name, "the channel")),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PreflightSummary renders findings as one line per issue for a tool result or a
// log. Empty when nothing would be refused, so a caller can treat "" as "clean".
func PreflightSummary(findings []PreflightFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Heads-up — on an unattended run this would be held back:\n")
	for _, f := range findings {
		b.WriteString("  • " + f.Name + ": " + f.Detail)
		if f.Fix != "" {
			b.WriteString(" " + f.Fix)
		}
		b.WriteString("\n")
	}
	b.WriteString("The schedule is still created; this is a warning, not a rejection.")
	return b.String()
}
