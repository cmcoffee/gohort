package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Pending approvals, in the conversation instead of only on the 🔑 tile.
//
// An approval is queued precisely when nobody is watching: an unattended fire
// wants a tool it isn't pre-authorized for, a scheduled run tries to message a
// real person. The record lands in the Authorizations store and the rail badge
// ticks up — and that was the whole surface. The work stalls somewhere the user
// isn't looking, and the only way to find out what stalled is to go read a list
// that names the agent but not the conversation it belongs to.
//
// So the cards are computed at session LOAD (not persisted): whatever is still
// pending for this agent renders as a card in the thread. Nothing to clean up
// when an approval is resolved elsewhere — it simply isn't in the next load.
// The buttons hit the same /api/console/approvals/{approve,always,deny}
// endpoints the pane uses, which re-check the owner server-side.

// pendingApprovalBlocks renders the owner's still-pending authorizations that
// belong to this agent — its own, plus any raised by a sub-agent it owns, since
// approving one of those grants at the parent anyway.
func pendingApprovalBlocks(udb Database, owner, agentID string) []UIBlock {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	var out []UIBlock
	for _, a := range ListAuthorizations(RootDB, owner) {
		if !approvalBelongsToAgent(udb, a, agentID) {
			continue
		}
		who, detail := approvalDisplay(udb, owner, a)
		data := map[string]string{
			"auth_id": a.ID,
			"action":  a.Action,
			"who":     who,
			"detail":  detail,
		}
		if approvalAlwaysMeans(a.Action) {
			data["always"] = "1"
		}
		out = append(out, UIBlock{
			Type:  "pending_approval",
			ID:    "approval-" + a.ID,
			Title: who,
			Text:  detail,
			Data:  data,
		})
	}
	return out
}

// approvalBelongsToAgent decides which conversation an authorization shows up
// in: the agent that raised it, or an agent that OWNS the one that raised it.
// The parent matters because an autonomous_tool approval is granted at the
// parent (autonomousApprovedSet walks the chain), so the parent's thread is a
// legitimate place to decide it. A build request shows to whoever ASKED for the
// build rather than the Builder that would run it — that's the conversation the
// user was having.
func approvalBelongsToAgent(udb Database, a Authorization, agentID string) bool {
	home := strings.TrimSpace(a.Agent)
	if a.Action == buildAgentAction && strings.TrimSpace(a.FromAgent) != "" {
		home = strings.TrimSpace(a.FromAgent)
	}
	if home == "" {
		return false
	}
	if home == agentID {
		return true
	}
	// Walk up from the raiser: is this agent one of its ancestors?
	seen := map[string]bool{}
	for id := home; id != "" && !seen[id]; {
		seen[id] = true
		rec, ok := loadAgent(udb, id)
		if !ok {
			return false
		}
		if rec.OwnedBy == agentID {
			return true
		}
		id = rec.OwnedBy
	}
	return false
}

// approvalAlwaysMeans reports whether "Always allow" does something DIFFERENT
// from a one-off approve for this action, so the card only offers the third
// button where it changes the outcome. For everything else resolveApproval
// returns before the always-branch — showing two buttons that do the same thing
// invites a standing grant the user didn't mean to give.
func approvalAlwaysMeans(action string) bool {
	switch action {
	case "send_message":
		return true // pre-authorizes that recipient
	case "", "delegate":
		return true // pre-authorizes delegation to that agent
	}
	return false
}
