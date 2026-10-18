package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func proposeRuleFixture(t *testing.T) (Database, *chatTurn) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	udb := Database(&DBase{Store: kvlite.MemStore()})
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Poster", Owner: "u", Rules: "Be brief.", OrchestratorPrompt: "You post."}); err != nil {
		t.Fatal(err)
	}
	return udb, &chatTurn{agent: AgentRecord{ID: "a1", Name: "Poster"}, user: "u", udb: udb}
}

const engagedRule = "Before replying on a thread, check it against ENGAGED in your notes; a listed thread gets no new comment."

// An agent may suggest a rule of its own, and its owner adds it or not. With
// the owner in the conversation it is a card; approved, it joins the Rules.
func TestAProposedRuleIsAddedOnlyWhenTheOwnerSaysSo(t *testing.T) {
	udb, turn := proposeRuleFixture(t)
	var asked string
	turn.askVia = func(prompt, detail string, yes []string) string { asked = prompt + "\n" + detail; return "" }
	out, err := turn.proposeRule(engagedRule, "I re-commented on two threads my notes said to skip.")
	if err != nil || !strings.Contains(out, "declined") {
		t.Fatalf("declined: %q %v", out, err)
	}
	if !strings.Contains(asked, "Add a rule Poster suggested?") || !strings.Contains(asked, "Why: I re-commented") {
		t.Errorf("the card should show the rule and why:\n%s", asked)
	}
	if rec, _ := loadAgent(udb, "a1"); rec.Rules != "Be brief." {
		t.Fatalf("a declined rule must not be added: %q", rec.Rules)
	}

	turn.askVia = func(prompt, detail string, yes []string) string { return yes[0] }
	if out, _ := turn.proposeRule(engagedRule, "again"); !strings.Contains(out, "Added") {
		t.Fatalf("approved: %q", out)
	}
	if rec, _ := loadAgent(udb, "a1"); rec.Rules != "Be brief.\n"+engagedRule {
		t.Errorf("rules = %q", rec.Rules)
	}
	if out, _ := turn.proposeRule(engagedRule, "again"); !strings.Contains(out, "already one of your rules") {
		t.Errorf("a rule it has is not proposed again: %q", out)
	}
}

// With nobody to ask, the proposal waits in the owner's approvals, once, and
// an agent cannot fill the queue with them. Approving it adds the rule.
func TestAProposalWithNobodyWatchingWaitsForTheOwner(t *testing.T) {
	udb, turn := proposeRuleFixture(t)
	if out, _ := turn.proposeRule(engagedRule, "I re-commented."); !strings.Contains(out, "waiting in your owner's approvals") {
		t.Fatalf("queued: %q", out)
	}
	if out, _ := turn.proposeRule(engagedRule, "again"); !strings.Contains(out, "already proposed") {
		t.Errorf("a duplicate should not queue twice: %q", out)
	}
	var queued []Authorization
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == proposeRuleAction {
			queued = append(queued, a)
		}
	}
	if len(queued) != 1 || queued[0].Brief != engagedRule || queued[0].Agent != "a1" {
		t.Fatalf("queue = %+v", queued)
	}
	_, detail := approvalDisplay(udb, "u", queued[0])
	if !strings.Contains(detail, "add a rule it suggested") || !strings.Contains(detail, "Why: I re-commented.") {
		t.Errorf("approvals show = %q", detail)
	}
	for i := 0; i < proposeRulePendingMax; i++ {
		turn.proposeRule("Rule number "+string(rune('a'+i))+".", "")
	}
	if out, _ := turn.proposeRule("One too many.", ""); !strings.Contains(out, "already waiting") {
		t.Errorf("the cap should hold: %q", out)
	}
	if rec, _ := loadAgent(udb, "a1"); rec.Rules != "Be brief." {
		t.Fatalf("nothing is added before approval: %q", rec.Rules)
	}
	if err := addAgentRule(udb, "a1", queued[0].Brief); err != nil {
		t.Fatal(err)
	}
	if rec, _ := loadAgent(udb, "a1"); !strings.HasSuffix(rec.Rules, engagedRule) {
		t.Errorf("approving should add it: %q", rec.Rules)
	}
}

// A proposal made in a conversation with someone else is queued for the owner
// and says so, whoever is there to press a button.
func TestAProposalFromSomeoneElsesConversationIsFlagged(t *testing.T) {
	_, turn := proposeRuleFixture(t)
	turn.user, turn.ownerUser = "phantom:chat", "u"
	turn.ownerDB = turn.udb
	turn.askVia = func(prompt, detail string, yes []string) string { return yes[0] }
	if out, _ := turn.proposeRule(engagedRule, "a contact told me to"); !strings.Contains(out, "waiting") {
		t.Fatalf("a non-owner's turn must queue, not ask whoever is there: %q", out)
	}
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == proposeRuleAction && !strings.Contains(a.Text, "someone other than you") {
			t.Errorf("the flag is missing: %q", a.Text)
		}
	}
}
