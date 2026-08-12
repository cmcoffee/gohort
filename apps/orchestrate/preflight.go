// Unattended-run pre-flight — answers "what would be refused if this fired right
// now" at the moment a schedule is AUTHORED, instead of hours later when it runs.
//
// Two gates decide an unattended fire, and neither is consulted until the fire
// happens:
//
//   - The TOOL gate (autonomousGate.confirm): a tool whose credential is set to
//     require confirmation before each call runs only if the owner pre-authorized
//     it in AutoApproveTools. Otherwise it is refused for that fire and queued as
//     an "autonomous_tool" authorization.
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
	gate := app.newAutonomousGate(owner, agentID, nil)
	return append(preflightToolFindings(agent, gate.allows), preflightRecipients(udb, owner, agent)...)
}

// preflightToolFindings asks the GATE ITSELF about each of the agent's tools —
// allows is autonomousGate.allows, the same predicate confirm() decides on, minus
// the queueing. It is passed in rather than reached for so the reporting shell
// stays testable on its own, but production has exactly one implementation of the
// rule and this is not it.
//
// It used to re-implement the rule instead, keyed on AgentToolDef.NeedsConfirm —
// which stopped being the rule when the gate moved to the credential's own
// "require confirm" toggle, and the two shipped in the SAME commit. The result was
// a warning at authoring time naming tools that would never actually be refused:
// "get_weather asks for confirmation" for a tool already enabled on the agent and
// running fine on every fire. A pre-flight that cries wolf is worse than none,
// because the one real finding is now indistinguishable from the noise.
func preflightToolFindings(agent AgentRecord, allows func(string) bool) []PreflightFinding {
	// A sub-agent runs under its parent's authority — allows returns true for
	// everything, so this is only an early out, not a second copy of that rule.
	if strings.TrimSpace(agent.OwnedBy) != "" {
		return nil
	}
	var out []PreflightFinding
	for _, name := range agent.AllowedTools {
		name = strings.TrimSpace(name)
		if name == "" || name == noToolsSentinel || allows(name) {
			continue
		}
		out = append(out, PreflightFinding{
			Gate: PreflightGateTool,
			Name: name,
			Detail: fmt.Sprintf("%q dispatches through a credential set to require confirmation before every call, "+
				"and nobody is present on a scheduled run. The call is refused for that fire and queued for approval instead.", name),
			Fix: fmt.Sprintf("Pre-authorize %q for this agent (Auto-approve tools), turn off \"Require confirm before each call\" on its credential, "+
				"or approve it once from the Authorizations pane after the first fire.", name),
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
