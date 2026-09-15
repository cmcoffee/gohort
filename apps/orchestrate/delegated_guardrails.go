// Restrictions travel DOWN a delegation, the way approvals already do.
//
// The asymmetry this closes: autonomousApprovedSet walks UP the OwnedBy chain
// so a sub-agent inherits every ancestor's AutoApproveTools, and the comment
// there is right about why — ownership is trust delegation. Nothing walked the
// other way. Permissions flowed downward and RESTRICTIONS did not, which is
// backwards from a safety standpoint: an agent secured because it handles
// something sensitive could hand that same material to an agent with no rules
// at all, and the owner's rules never saw the handoff.
//
// The doctrine is already written in pipeline_guardrail.go: what binds an agent
// has to bind what it delegates. That file guards the pipeline and machine
// doors on the reasoning that a recipe has no rules of its own. The agent door
// was left out on the opposite reasoning — that a distinct agent has its own
// rules and those apply instead — which holds only when the target HAS rules. A
// Builder-made sub-agent usually has none, and enforceSubAgentPosture sets
// posture without setting policy.
//
// So the guardrails ride along. Guardrails and not Rules: Rules is a prompt
// signal the agent itself can be talked past, while Guardrails is judged by an
// independent warden that never saw the conversation. Inheriting the soft half
// would have looked like the same fix and enforced nothing.
//
// The invariant that keeps Guardrails trustworthy is preserved: the field is
// settable only through the owner's editor form, never by an LLM tool, so a
// persuaded agent cannot rewrite the rule it is about to be checked against.
// Nothing here writes to the store. The union exists for the length of one
// delegated run, on a copy.

package orchestrate

import (
	"sort"
	"strings"
)

// inheritDelegatorGuardrails returns the target as it should run for THIS
// delegation: its own guardrails plus the delegator's, enforced at least where
// the delegator enforced them.
//
// Operates on a copy. Nothing is written back to the store — an inherited rule
// belongs to the delegation, not to the agent, and a target that kept them
// would carry one delegator's policy into every later run by anybody.
//
// Accumulates down a chain by construction: B running under A's rules IS the
// record a further dispatch reads, so C gets A's and B's. Deduped, or a loop
// between two agents would grow the same line forever.
func inheritDelegatorGuardrails(from, target AgentRecord) AgentRecord {
	// A delegator that has SUSPENDED its own rules is not handing them on. The
	// owner set them aside deliberately (GuardrailsDisabled keeps them authored
	// and stops enforcing them), and inheriting them here would enforce through
	// a sub-agent exactly what was switched off on the parent.
	if from.GuardrailsDisabled {
		return target
	}
	add := ruleLines(from.Guardrails)
	if len(add) == 0 {
		return target
	}
	have := map[string]bool{}
	for _, line := range ruleLines(target.Guardrails) {
		have[strings.ToLower(line)] = true
	}
	var fresh []string
	for _, line := range add {
		if key := strings.ToLower(line); !have[key] {
			have[key] = true
			fresh = append(fresh, line)
		}
	}
	if len(fresh) == 0 {
		return target
	}
	// Hooks BEFORE the rules are merged: resolveGuardrailHooks reports nothing
	// for an agent with no rules, and the target is about to stop being one.
	// Reading it after would give the inherited rules the target's default
	// points rather than the union, which is how a rule the delegator judged on
	// every reply ends up judged only before a tool call.
	fromHooks := resolveGuardrailHooks(from)
	targetHooks := resolveGuardrailHooks(target)

	// A target whose own rules are SUSPENDED still gets the delegator's, and
	// only the delegator's. The suspension is the owner setting that agent's
	// own rules aside; it is not a licence to receive work another agent's
	// rules cover, and it is not an occasion to switch its own back on. So the
	// inherited lines replace rather than join, and the flag clears with the
	// target's own rules no longer in the field.
	if target.GuardrailsDisabled {
		target.Guardrails = strings.Join(fresh, "\n")
		target.GuardrailsDisabled = false
	} else {
		target.Guardrails = strings.TrimSpace(strings.TrimSpace(target.Guardrails) + "\n" + strings.Join(fresh, "\n"))
	}

	hooks := map[string]bool{}
	for h := range fromHooks {
		hooks[h] = true
	}
	for h := range targetHooks {
		hooks[h] = true
	}
	// The target's own declared list too: an owner who named points for rules
	// they have not written yet still named them.
	for _, h := range target.GuardrailHooks {
		if h = strings.TrimSpace(h); h != "" {
			hooks[h] = true
		}
	}
	target.GuardrailHooks = sortedHooks(hooks)
	return target
}

// ruleLines splits a guardrails field into its non-empty lines. One rule per
// line is the authored format.
func ruleLines(rules string) []string {
	var out []string
	for _, line := range strings.Split(rules, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// sortedHooks renders the merged set as the record's explicit list.
//
// Materializing it matters: an empty GuardrailHooks means "the default set", so
// leaving it empty after a merge would re-derive the default and drop whichever
// points the delegator had turned on beyond it. Empty in means empty out, which
// leaves the record's own default in place.
func sortedHooks(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
