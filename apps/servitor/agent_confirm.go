// What happens when an AGENT hits the confirmation gate.
//
// The gate was written for a human at servitor's console: it parks the command,
// emits a confirm event on the session's SSE stream, and blocks on a channel
// that /api/chat/v2/confirm feeds when someone clicks. That is exactly right
// for the console and useless for anything else — an agent driving an appliance
// has nobody watching that stream, so the command would sit for five minutes
// and fail with "confirmation timed out".
//
// Worse than the delay is what the delay pushes you toward. The only way to
// make agent-driven appliance work usable would be to grant broad auto-run
// categories up front, so the safe configuration would be the unusable one and
// the gate would be hollowed out by whoever wanted the feature to work.
//
// So an agent gets a REFUSAL rather than a wait. It is immediate, it says
// exactly what would permit the command, and the agent can relay that to the
// person who asked. Nothing hangs, and nothing is approved by a timeout.
//
// Deliberately NOT an approval queue. Queuing a request that unblocks a
// goroutine somewhere else needs a protocol spanning two lifecycles — the exec
// waiting and the user answering minutes later, possibly after the turn ended —
// and a record with no consumer is the mistake this file exists to avoid
// repeating. The refusal is honest on its own; inline approval can be built on
// top of it later without changing what happens today.
package servitor

import (
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core/textutil"
)

// agentCommandRefusal is what an acting agent is told instead of being made to
// wait. It names the four things needed to act on it: what was refused, why,
// where, and what would change the answer.
//
// The category is spelled out because it is the unit a grant is written in —
// "needs approval" sends someone hunting, "needs pkg_install on lab-box" is a
// setting they can find.
func agentCommandRefusal(cmd string, cat RiskCategory, reason, appliance string) error {
	where := strings.TrimSpace(appliance)
	if where == "" {
		where = "this system"
	}
	detail := strings.TrimSpace(reason)
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Errorf("not run: %q is %s%s on %s, and you have no standing permission for that. "+
		"Nothing was executed and nothing is waiting: say so plainly and tell the person that the owner can allow %s for you on %s, "+
		"or run it themselves from Servitor. Do NOT retry it, reword it, or try to get the same effect another way",
		truncateCmd(cmd, 160), string(cat), detail, where, string(cat), where)
}

// applianceLabel is what a person calls this box: its name when it has one,
// its id otherwise. Local rather than borrowed, so a refusal never renders as
// "on ".
func applianceLabel(name, id string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if i := strings.TrimSpace(id); i != "" {
		return i
	}
	return "this system"
}

// truncateCmd keeps a refusal readable when the command is a long pipeline.
// The head is what identifies it; the tail is usually arguments.
func truncateCmd(s string, max int) string {
	s = strings.TrimSpace(s)
	return textutil.Clip(s, max)
}

// readOnlyDecline is what an investigation that nobody can answer is told
// when a command needs approval: the read-only runs another app starts
// (Guides grounding a section, a workspace's fan-out). They used to raise a
// card anyway, which the run had already declined before anyone could click
// it, and report the decline as "denied by user", which nobody had said.
func readOnlyDecline(cmd string, cat RiskCategory, reason, appliance string) error {
	where := strings.TrimSpace(appliance)
	if where == "" {
		where = "this system"
	}
	detail := strings.TrimSpace(reason)
	if detail != "" {
		detail = " (" + detail + ")"
	}
	return fmt.Errorf("not run: %q is %s%s on %s, and this investigation is read-only, so nobody is asked to approve it. "+
		"Nothing was executed and nothing is waiting. Answer from what you can find without it, and say plainly that this step needs the owner",
		truncateCmd(cmd, 160), string(cat), detail, where)
}

// confirmAnswerable reports whether a person can answer this run's approval
// cards: the console's own runs register their channel as interactive.
func confirmAnswerable(sid string) bool {
	v, ok := confirmChans.Load(sid)
	if !ok {
		return false
	}
	p, ok := v.(pendingConfirm)
	return ok && p.interactive && p.ch != nil
}
