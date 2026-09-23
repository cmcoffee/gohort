package orchestrate

// A ceiling the thing under it can raise is not a ceiling.
//
// The Limits tab called these "ceilings the framework keeps, not rules the
// agent is asked to follow", which was true of how they are enforced and false
// of how they are set: update_agent wrote all three through mergeAgentArgs.

import (
	"strings"
	"testing"
)

func TestAnAgentMayTightenItsOwnCeilingsAndNotRaiseThem(t *testing.T) {
	before := AgentRecord{
		ID: "a", ForcePrivate: true, DailySpendUSD: 5,
		ActionQuotas: ActionQuotaMap{"send_email": 6, "post": 3},
	}

	// Tightening passes through untouched, in every direction that counts as
	// tighter. Refusing this would make the safe change the awkward one.
	tighter := AgentRecord{
		ID: "a", ForcePrivate: true, DailySpendUSD: 2,
		ActionQuotas: ActionQuotaMap{"send_email": 1, "post": 3},
	}
	if notes := keepEnforcementTight(before, &tighter); len(notes) != 0 {
		t.Errorf("tightening was reverted: %v", notes)
	}
	if tighter.DailySpendUSD != 2 || tighter.ActionQuotas["send_email"] != 1 {
		t.Error("a tightening update did not stick")
	}

	// Raising every one of them at once: each is put back, and each says so.
	loose := AgentRecord{
		ID: "a", ForcePrivate: false, DailySpendUSD: 50,
		ActionQuotas: ActionQuotaMap{"send_email": 99},
	}
	notes := keepEnforcementTight(before, &loose)
	if !loose.ForcePrivate {
		t.Error("an agent turned off its own privacy lock")
	}
	if loose.DailySpendUSD != 5 {
		t.Errorf("the spend cap was raised to %g", loose.DailySpendUSD)
	}
	if loose.ActionQuotas["send_email"] != 6 {
		t.Errorf("an action limit was raised to %d", loose.ActionQuotas["send_email"])
	}
	// A quota the update DROPPED is restored too: removing the entry is the
	// easiest way to raise a limit to infinity.
	if loose.ActionQuotas["post"] != 3 {
		t.Error("an action limit was removed rather than raised, and stayed removed")
	}
	if len(notes) != 3 {
		t.Errorf("want one note per ceiling, got %d: %v", len(notes), notes)
	}
	joined := strings.Join(notes, " | ")
	for _, want := range []string{"force_private", "daily_spend_usd", "send_email", "post"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the reply does not say %q did not move: %s", want, joined)
		}
	}

	// Clearing the cap entirely is the loosest value, not the smallest.
	cleared := AgentRecord{ID: "a", ForcePrivate: true, DailySpendUSD: 0}
	keepEnforcementTight(before, &cleared)
	if cleared.DailySpendUSD != 5 {
		t.Error("0 means uncapped, so clearing the cap is loosening and must not stick")
	}

	// An agent that had no ceiling can be given one, and privacy can be turned
	// ON. The ratchet only ever holds a restriction in place.
	fresh := AgentRecord{ID: "b"}
	added := AgentRecord{ID: "b", ForcePrivate: true, DailySpendUSD: 10,
		ActionQuotas: ActionQuotaMap{"post": 2}}
	if notes := keepEnforcementTight(fresh, &added); len(notes) != 0 {
		t.Errorf("setting a first ceiling was treated as loosening: %v", notes)
	}
	if !added.ForcePrivate || added.DailySpendUSD != 10 || added.ActionQuotas["post"] != 2 {
		t.Error("a first ceiling did not stick")
	}
}

// The reverts reach the model, in words that send the person to where the
// ceiling CAN be changed rather than leaving it to retry.
func TestTheAgentIsToldWhatDidNotMove(t *testing.T) {
	if enforcementNote(nil) != "" {
		t.Error("a clean update carries a note about ceilings")
	}
	note := enforcementNote([]string{"force_private stays ON"})
	for _, want := range []string{"Security -> Limits", "rather than trying again"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note is missing %q: %s", want, note)
		}
	}

	// Wired into the update path, on the record as it stood BEFORE the merge.
	src := mustReadFile(t, "agent_crud_tools.go")
	if !strings.Contains(src, "keepEnforcementTight(priorLimits, &existing)") {
		t.Error("update_agent does not ratchet, so an agent can raise its own ceilings again")
	}
	if !strings.Contains(src, "enforcementNote(raised)") {
		t.Error("a revert happens silently, so the model retries instead of asking")
	}
	// create_agent is deliberately NOT ratcheted: a new agent's first value is
	// a choice, not a raise.
	if strings.Contains(src, "keepEnforcementTight(AgentRecord{}") {
		t.Error("create_agent was ratcheted, so Builder cannot configure a new agent")
	}
}

// The lock is the control that makes every other control on the page stick, so
// it is ON the page. It lived only as an icon on the editor, which is the one
// place somebody securing an agent was told not to look.
func TestTheLockIsOfferedWhereTheOtherControlsAre(t *testing.T) {
	page := mustReadFile(t, "page_agent_access.go")
	if !strings.Contains(page, `{Field: "locked", Type: "toggle"`) {
		t.Error("Security does not offer the lock, so the control that protects the others is elsewhere")
	}
	// Its OWN endpoint. Locked is deliberately not patchable - setAgentLocked
	// is the single door - so a form posting it to the agent PATCH would be
	// dropped and the toggle would revert on the next reload.
	if !strings.Contains(page, "lockURL := patchURL + \"/lock\"") {
		t.Error("the lock toggle does not post to the lock endpoint")
	}
	if !strings.Contains(page, "PostURL: lockURL") {
		t.Error("the lock panel posts somewhere other than the lock endpoint")
	}
	if patchAgentFields["locked"] {
		t.Error("locked became patchable, so an ordinary form save can now clobber a lock")
	}
}
