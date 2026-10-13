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
		// Not minted per turn: the card the second turn updates has to be the
		// card the first turn drew. workPlanChanged paints the session's card,
		// which this names only for a session that has not had one yet.
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
//
// The card is the session's, not the spec's fixed id: a plan committed after
// the last one finished is drawn on a card of its own, so the finished one
// stays where it was instead of being rewritten with the new steps.
func (t *chatTurn) workPlanChanged(c WorkPlanChange) {
	card := t.session.WorkPlanCard
	if card == "" {
		card = c.PlanID
	}
	if c.Kind == "set" {
		t.toolMu.Lock()
		prev := t.session.keptPlanCard(card)
		open := prev != nil && planStepsOpen(prev.Plan)
		t.toolMu.Unlock()
		card = t.nextPlanCard(card, open, "workplan:")
	}
	t.session.WorkPlanCard = card
	t.paintPlanCard(card, workPlanSteps(c.Steps), c.Reason)
	t.saveSession()
}

// restoreWorkPlanCard re-paints the checklist at the head of a turn that
// inherits one with steps still open, so a conversation resumed in a new
// browser tab shows the plan it is actually working rather than a blank pane
// until the next movement. A finished plan is left where it is: re-painting
// it drew it again at the end of the thread.
func (t *chatTurn) restoreWorkPlanCard() {
	if !t.agent.WorkPlan || t.session == nil || t.session.WorkPlan == nil {
		return
	}
	if t.session.WorkPlan.Pending() == 0 {
		return
	}
	card := t.session.WorkPlanCard
	if card == "" {
		card = "workplan:" + t.session.ID
		t.session.WorkPlanCard = card
	}
	t.paintPlanCard(card, workPlanSteps(t.session.WorkPlan.Snapshot()), "")
}
