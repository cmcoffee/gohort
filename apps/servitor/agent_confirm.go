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

// agentRefusal is what an acting agent is told instead of being made to
// wait. It names what was refused, why, where, and what would change the
// answer.
//
// The category is spelled out because it is the unit a grant is written in:
// "needs approval" sends someone hunting, "needs pkg_install on lab-box" is a
// setting they can find.
//
// Near is a rule for the same program that the command missed the shape of;
// Covered and Rest split a line a rule covers only part of. Either way the
// agent is told how to run what is allowed, rather than only "no": told only
// that it had no permission, an agent that added a flag in front of the
// program, or an exit check after it, gave up on a command the owner had
// allowed.
type agentRefusal struct {
	Cmd       string
	Cat       RiskCategory
	Reason    string
	Appliance string
	Near      string
	Covered   string
	Rest      string
}

// refuseRetry is how every refusal ends. Refused for now, not for good: a
// refusal kept in a long conversation (an MCP thread) was read later as a
// standing answer, and the agent repeated it instead of asking again after
// the owner had allowed the command. Rewording it to get round the gate is
// still out.
const refuseRetry = "This is the answer for now: permissions can change, so if you are asked again later, try it again rather than repeating this. Do NOT reword it or reach the same effect another way to get round this check"

func (r agentRefusal) err() error {
	where := strings.TrimSpace(r.Appliance)
	if where == "" {
		where = "this system"
	}
	detail := strings.TrimSpace(r.Reason)
	if detail != "" {
		detail = " (" + detail + ")"
	}
	cmd, cat := truncateCmd(r.Cmd, 160), string(r.Cat)
	if c, rest := strings.TrimSpace(r.Covered), strings.TrimSpace(r.Rest); c != "" && rest != "" {
		return fmt.Errorf("not run: in %q, the owner's rules on %s allow %s, but the rest of the line, %q, is %s%s and needs approval. "+
			"Nothing was executed. Run the allowed part without the rest if that does what is needed; it then runs without asking. "+
			"If the rest is needed, say so plainly and tell the person the owner can allow %s for you on %s. %s",
			cmd, where, c, rest, cat, detail, cat, where, refuseRetry)
	}
	if near := strings.TrimSpace(r.Near); near != "" {
		return fmt.Errorf("not run: %q is not in the shape of the rule the owner allowed on %s for that program: %s. "+
			"Run it in exactly that shape if that does what is needed (nothing added in front of the program, words only where the rule has them); it then runs without asking. "+
			"If it cannot be done in that shape, say so plainly and tell the person the owner can widen the rule or allow %s for you on %s. %s",
			cmd, where, near, cat, where, refuseRetry)
	}
	return fmt.Errorf("not run: %q is %s%s on %s, and you have no standing permission for that. "+
		"Nothing was executed and nothing is waiting: say so plainly and tell the person that the owner can allow %s for you on %s, "+
		"or run it themselves from Servitor. %s",
		cmd, cat, detail, where, cat, where, refuseRetry)
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
