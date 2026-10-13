package orchestrate

// Where a plan's card stays.
//
// A plan is drawn as one card that updates in place as its steps move. The
// card used to live only on the page: nothing kept it with the thread, so a
// reload dropped it, and the next paint (a tracked plan re-paints its card at
// the head of every turn, the build check repainted a finished plan) drew it
// again at the very end of the conversation. A finished plan read as having
// jumped to the bottom.
//
// Now every movement is also kept on the session as a block stamped with the
// time the card was first drawn, and a reload puts it back among the messages
// at that time. A finished plan is not painted again, and a new plan gets a
// card of its own rather than rewriting the last one's, wherever that was.

import (
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// planStepsOpen reports whether any step is still to do.
func planStepsOpen(steps []BuildPlanStep) bool {
	for _, s := range steps {
		if s.Status == "pending" || s.Status == "in_progress" {
			return true
		}
	}
	return false
}

// nextPlanCard is the card a newly committed plan is drawn on: the current one
// while it still has steps open, since a plan replaced midway is the same piece
// of work; otherwise a fresh one, so the plan before keeps its card where it
// finished.
func (t *chatTurn) nextPlanCard(current string, open bool, prefix string) string {
	if current != "" && open {
		return current
	}
	return prefix + t.session.ID + "-" + UUIDv4()[:8]
}

// paintPlanCard sends one movement of a plan to the page and keeps it on the
// session, so the card survives a reload in the place it was first drawn.
func (t *chatTurn) paintPlanCard(id string, steps []BuildPlanStep, reason string) {
	if t == nil || id == "" {
		return
	}
	if t.sse != nil {
		payload := map[string]any{
			"kind": "block",
			"type": "orchestrate_plan",
			"id":   id,
			"plan": steps,
			// Stamp to help the activity log show update ordering when
			// the same block id receives multiple events.
			"emitted_at": time.Now().UTC().Format(time.RFC3339),
		}
		// A revision says WHY the plan changed. It rides the event rather
		// than the steps because it is about the change, not any one step.
		if reason != "" {
			payload["reason"] = reason
		}
		t.sse.Send(payload)
	}
	if t.session == nil {
		return
	}
	kept := append([]BuildPlanStep(nil), steps...)
	t.toolMu.Lock()
	defer t.toolMu.Unlock()
	at := time.Now()
	if prev := t.session.keptPlanCard(id); prev != nil && !prev.At.IsZero() {
		at = prev.At
	}
	t.session.upsert_ui_block(UIBlock{Type: "orchestrate_plan", ID: id, Plan: kept, At: at}, nil)
}

// keptPlanCard is the plan card kept under id, or nil.
func (s *ChatSession) keptPlanCard(id string) *UIBlock {
	for i := range s.UIBlocks {
		if s.UIBlocks[i].Type == "orchestrate_plan" && s.UIBlocks[i].ID == id {
			return &s.UIBlocks[i]
		}
	}
	return nil
}

// workPlanSteps is a tracked plan's snapshot in the card's shape.
func workPlanSteps(steps []WorkPlanStep) []BuildPlanStep {
	out := make([]BuildPlanStep, 0, len(steps))
	for _, s := range steps {
		out = append(out, BuildPlanStep{
			Number:        s.ID,
			Title:         s.Title,
			WhatToFind:    s.WhatToFind,
			Status:        string(s.Status),
			Findings:      s.Findings,
			BlockedReason: s.BlockedReason,
		})
	}
	return out
}
