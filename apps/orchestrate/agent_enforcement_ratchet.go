// An agent may tighten its own ceilings. It may not raise them.
//
// The Limits tab says these are "ceilings the framework keeps, not rules the
// agent is asked to follow", and that was true of how they are ENFORCED and
// false of how they are set: update_agent writes action_quotas,
// daily_spend_usd and force_private through mergeAgentArgs, so an agent
// holding the authoring toolset could clear its own spend cap or turn privacy
// off. A ceiling the thing under it can raise is not a ceiling.
//
// The fix is a ratchet, not a refusal. Tightening is always allowed: an agent
// deciding it needs less is a decision nobody has to review, and refusing it
// would make the safe direction the awkward one. create_agent is untouched -
// Builder configuring a NEW agent is choosing a first value, not raising one
// somebody set.
//
// Same direction as every other restriction here. applyForcePrivateToDispatch
// ANDs rather than replaces, guardrails and the never-unattended mark inherit
// downward, and a dispatch narrows and never widens. This is that rule applied
// to an agent editing itself.
//
// Reverting rather than erroring, and saying so in the reply. A refused update
// would lose the OTHER fields in the same call, which are usually the point of
// it - and the model gets told exactly what did not move and who can move it,
// so it can pass that on instead of retrying.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"
)

// keepEnforcementTight reverts any ceiling the update loosened and returns one
// line per revert, for the reply. Empty means the update raised nothing.
func keepEnforcementTight(before AgentRecord, next *AgentRecord) []string {
	if next == nil {
		return nil
	}
	var notes []string

	// Privacy is a direction, not a value: ForcePrivate exists to be the
	// answer that does not move. The runtime endpoint already refuses to turn
	// it off (see handleAgentPrivateMode); this closes the door the tool had.
	if before.ForcePrivate && !next.ForcePrivate {
		next.ForcePrivate = true
		notes = append(notes, "force_private stays ON: an agent cannot turn off its own privacy lock")
	}

	// 0 means uncapped, so it is the loosest value rather than the smallest.
	if before.DailySpendUSD > 0 &&
		(next.DailySpendUSD == 0 || next.DailySpendUSD > before.DailySpendUSD) {
		notes = append(notes, fmt.Sprintf(
			"daily_spend_usd stays at %g: an agent may lower its own spend cap, not raise or clear it",
			before.DailySpendUSD))
		next.DailySpendUSD = before.DailySpendUSD
	}

	// Per action, because a call that tightens one and drops another is the
	// shape this is most likely to arrive in. A quota the update did not
	// mention is restored; one it lowered is kept; one it raised is put back.
	var raised []string
	for action, cap := range before.ActionQuotas {
		got, ok := next.ActionQuotas[action]
		if ok && got <= cap {
			continue
		}
		if next.ActionQuotas == nil {
			next.ActionQuotas = ActionQuotaMap{}
		}
		next.ActionQuotas[action] = cap
		raised = append(raised, action)
	}
	if len(raised) > 0 {
		sort.Strings(raised)
		notes = append(notes, fmt.Sprintf(
			"action limits restored for %s: an agent may lower its own limits, not raise or remove them",
			strings.Join(raised, ", ")))
	}
	return notes
}

// enforcementNote turns the reverts into the sentence the model is told, in
// words that route the person to where the ceiling CAN be changed rather than
// leaving the model to retry.
func enforcementNote(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " Some ceilings were not changed - " + strings.Join(notes, "; ") +
		". These are set by the owner under Security -> Limits; say so rather than trying again."
}
