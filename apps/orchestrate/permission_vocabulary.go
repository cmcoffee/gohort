// The words a permission decision is offered in, defined once.
//
// There were three sets for one idea. A tool's unattended ladder said Runs /
// Queues / Never, the standing-decision rows said Always allow / Needs
// approval / Blocked, and whether a tool stops to ask in chat was a switch
// with no words at all. Same three values underneath - allow, ask, block - and
// a reader had to learn that three times and work out they were the same
// question about different things.
//
// Two of those were defensible on their own. "Queues" is accurate: an
// unattended run has nobody watching, so asking means filing a request rather
// than stopping and waiting. "Blocked" reads well beside a contact. But being
// right one row at a time is what produced a surface where nothing looked like
// anything else, and the difference belongs in the help text beside a control,
// not in a different word for the same decision.
package orchestrate

import "github.com/cmcoffee/gohort/core/ui"

// The three, in the order they escalate: freely, with a person, never.
//
// "Ask first" rather than "Needs approval" because it says who does what, and
// it stays true in both situations: in chat the turn stops and waits for you;
// unattended it files a request you answer later. Both are asking first.
//
// "Never" rather than "Blocked" because a ladder reads as a rule about future
// calls, and "Blocked" reads as a state something is already in.
func permissionLadder() []ui.SelectOption {
	return []ui.SelectOption{
		{Value: "allow", Label: "Always allow"},
		{Value: "ask", Label: "Ask first"},
		{Value: "block", Label: "Never"},
	}
}

// permissionLadderNoNever is the same ladder for a row whose only two states
// are allow and ask.
//
// Not a different control: the same words, one segment short. A row that
// cannot hold "Never" used to get a bare switch instead, which asked the
// reader to recognise the same decision in a second shape.
func permissionLadderNoNever() []ui.SelectOption {
	return permissionLadder()[:2]
}
