// The tracked plan, mounted on a conversation.
//
// core owns the plan and its six tools (core/plan.go, core/plan_tools.go). This
// is the half a conversation adds: the checklist is PERSISTED on the session, so
// a plan committed to in one message is still the plan in the next, and every
// movement paints the live card the browser already knows how to draw.
//
// The renderer is not new. `orchestrate_plan` has drawn a checklist with
// per-step status, findings and blocked reasons since Builder's build plan, and
// its payload is step-for-step what a plan snapshot is — so a host that emits a
// snapshot gets the card, and re-emitting under the same id updates it in place.

package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

// workPlanTools mounts the group for this turn, or returns nothing when the
// agent does not carry a tracked plan.
//
// Built at most once per turn and cached, because the tools close over ONE
// plan: a second group would give the same conversation two checklists that
// overwrite each other's card.
func (t *chatTurn) workPlanTools() []AgentToolDef {
	if !t.agent.WorkPlan || t.session == nil {
		return nil
	}
	if t.workPlan != nil {
		return t.workPlan.All()
	}
	plan := t.session.WorkPlan
	if plan == nil {
		plan = &WorkPlan{}
		t.session.WorkPlan = plan
	}
	set := WorkPlanTools(WorkPlanToolSpec{
		Plan: plan,
		// Keyed on the SESSION, not minted per turn: one conversation has one
		// checklist, and the card the second turn updates has to be the card the
		// first turn drew.
		PlanID:   "workplan:" + t.session.ID,
		OnChange: t.workPlanChanged,
	})
	t.workPlan = &set
	return set.All()
}

// workPlanChanged paints the card and writes the plan down.
//
// Both, on every movement, and the order matters: the card is what the person
// is watching, and the save is what makes the plan survive a turn boundary, a
// reload, or the browser going away mid-run.
func (t *chatTurn) workPlanChanged(c WorkPlanChange) {
	t.emitWorkPlanBlock(c)
	t.saveSession()
}

// emitWorkPlanBlock sends the plan as the orchestrate_plan block the browser
// already renders. Same id every time, so the card updates rather than stacking.
func (t *chatTurn) emitWorkPlanBlock(c WorkPlanChange) {
	if t.sse == nil {
		return
	}
	items := make([]map[string]any, 0, len(c.Steps))
	for _, s := range c.Steps {
		item := map[string]any{
			"id":     s.ID,
			"title":  s.Title,
			"status": string(s.Status),
		}
		if s.WhatToFind != "" {
			item["what_to_find"] = s.WhatToFind
		}
		if s.Findings != "" {
			item["findings"] = s.Findings
		}
		if s.BlockedReason != "" {
			item["blocked_reason"] = s.BlockedReason
		}
		items = append(items, item)
	}
	payload := map[string]any{
		"kind": "block",
		"type": "orchestrate_plan",
		"id":   c.PlanID,
		"plan": items,
	}
	// A revision says WHY the plan changed. It rides the event rather than the
	// steps because it is about the change, not about any one step.
	if c.Reason != "" {
		payload["reason"] = c.Reason
	}
	t.sse.Send(payload)
}

// restoreWorkPlanCard re-paints the checklist at the head of a turn that
// inherits one, so a conversation resumed in a new browser tab shows the plan it
// is actually working rather than a blank pane until the next movement.
func (t *chatTurn) restoreWorkPlanCard() {
	if !t.agent.WorkPlan || t.session == nil || t.session.WorkPlan == nil {
		return
	}
	if !t.session.WorkPlan.IsSet() {
		return
	}
	t.emitWorkPlanBlock(WorkPlanChange{
		PlanID: "workplan:" + t.session.ID,
		Kind:   "set",
		Steps:  t.session.WorkPlan.Snapshot(),
	})
}
