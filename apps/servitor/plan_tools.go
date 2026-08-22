package servitor

import (
	. "github.com/cmcoffee/gohort/core"
)

// The investigation plan, in servitor's words.
//
// The checklist itself — steps, the pending/in_progress/done/blocked lifecycle,
// the revision cap, the gap report that keeps an unfinished step from
// evaporating out of the final answer — lives in core (core/plan.go,
// core/plan_tools.go). It started here, and it was the most complete of the four
// planners this codebase had grown, which is why it was lifted rather than
// copied for the fifth.
//
// What stays here is what is actually SERVITOR's about it: that a step's work is
// a probe, and that Map REQUIRES a plan (mapping a system is the plan) while
// chat opts into one (most questions are a single probe, and forcing a five-step
// plan onto "what port is nginx on" would tax every follow-up).
type planToolSet = WorkPlanToolSet

// buildPlanTools constructs the plan group for one session.
func buildPlanTools(id string, required bool) planToolSet {
	// One id per plan INSTANCE, not per session: chat builds a fresh group on
	// every turn, so a follow-up investigation must post its own checklist card
	// rather than rewriting the previous investigation's.
	return WorkPlanTools(WorkPlanToolSpec{
		Plan:           &WorkPlan{},
		PlanID:         cheapID(),
		SetDescription: planSetDescription(required),
		WorkHint:       "Delegate probe(s) to investigate.",
		// Plan events stream to the UI for the left-pane checklist render. The
		// set gets its own event kind because the pane opens a card on it.
		OnChange: func(c WorkPlanChange) {
			kind := "plan_step"
			if c.Kind == "set" {
				kind = "plan_set"
			}
			emit(id, probeEvent{Kind: kind, Plan: c.Steps, Reason: c.Reason, PlanID: c.PlanID})
		},
	})
}

// planSetDescription is the brief for set_plan on each of servitor's two
// surfaces. Map's is a REQUIREMENT with sizing guidance for an appliance; chat's
// is an invitation, because a plan there is what the investigator reaches for
// when a question turns out to be bigger than it looked.
func planSetDescription(required bool) string {
	if required {
		return "REQUIRED FIRST CALL — emit a structured investigation plan before any other tool. " +
			"Each step has a short title (5–10 words) and a what_to_find description (1–3 sentences) explaining what success looks like for that step. " +
			"Order steps by dependency: foundation/discovery first, then deeper investigation that builds on it. " +
			"Typically 5–12 steps for a generic system; scale up to 15+ for complex appliances (Kubernetes hosts, multi-tenant DB servers, hosts running many distinct services). " +
			"Err toward more steps with narrower scopes rather than fewer steps with sprawling scopes — narrow steps produce sharper findings and clearer gap reports. " +
			"You can revise step status as you go, but the initial plan is your contract — use revise_plan only if findings reveal a step you couldn't have known to include."
	}
	return "Commit to a multi-step investigation plan, and track it. Use when answering needs SEVERAL distinct findings that build on each other — not for a question one probe can settle. " +
		"Each step has a short title (5-10 words) and a what_to_find description (1-3 sentences) saying what success looks like. Order by dependency: discovery first, then what builds on it. " +
		"Typically 3-8 steps for a focused question. Once set, work the steps with mark_step_in_progress / record_step_findings / mark_step_blocked, and call report_gaps before your final answer so unresolved steps are stated rather than quietly dropped. " +
		"A plan is a commitment the user can see in the checklist — set one when the work deserves it, skip it when a single probe answers the question."
}
