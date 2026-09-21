package orchestrate

// The mark on a turn a rule stopped, and what saying "that was wrong" sends.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// One mark per turn, not per block: a rule caught at pre_action and again at
// pre_output is one stopped reply, and two glyphs would say how many times it
// was caught, which is the detail this deliberately does not carry.
func TestATurnIsMarkedOnce(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.session = &ChatSession{ID: "s1"}
	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}

	turn.turnDiag("guardrail-blocked", aRuleAndItsReason)
	turn.turnDiag("guardrail-blocked", "and again at another hook")
	turn.deliverBlockedMark()

	if n := strings.Count(buf.String(), `"message_mark"`); n != 1 {
		t.Errorf("the turn carries %d marks, want 1:\n%s", n, buf.String())
	}
	// It says what happened and nothing about which rule.
	if !strings.Contains(buf.String(), "This action was blocked") {
		t.Errorf("the mark says nothing:\n%s", buf.String())
	}
	for _, leak := range []string{"never tell jokes", "CEO", "pre_output"} {
		if strings.Contains(buf.String(), leak) {
			t.Errorf("the mark leaked %q", leak)
		}
	}
	// It rides the reply as a stored field, which is what brings it back on a
	// reload, and it names the action the app registers.
	mark := turn.blockedMark()
	if mark == nil || mark.Glyph != "!" || mark.Action != blockedMarkAction {
		t.Fatalf("the stored mark is not the one the panel draws: %+v", mark)
	}
	if mark.Data["session"] != "s1" {
		t.Errorf("the mark lost the session the report needs: %+v", mark.Data)
	}
}

// Nothing to mark on a turn no rule stopped.
func TestAnUnstoppedTurnCarriesNoMark(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.session = &ChatSession{ID: "s1"}
	if m := turn.blockedMark(); m != nil {
		t.Errorf("an unstopped turn was marked: %+v", m)
	}
}

// The owner's own run gets the card, not the mark.
func TestTheOwnersTurnIsNotMarked(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "alice", "a1", "s1")
	turn.session = &ChatSession{ID: "s1"}
	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}
	turn.turnDiag("guardrail-blocked", aRuleAndItsReason)

	turn.deliverBlockedMark()
	if strings.Contains(buf.String(), `"message_mark"`) {
		t.Error("the owner got a mark instead of their own card")
	}
	if len(noticeFrames(t, buf.String())) != 1 {
		t.Error("the owner lost the card")
	}
}

// The report carries the guardrail's OWN diagnostic, read from the owner's
// log, and not the recipient's conversation.
func TestTheReportCarriesTheReasonAndNotTheTurn(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.session = &ChatSession{ID: "s1"}
	turn.recordGuardrailBlock("never mention project atlas", guardHookPreOutput, "the draft named it")

	blocks := blocksForTurn(UserDB(root, "alice"), "a1", "s1", 3)
	if len(blocks) != 1 {
		t.Fatalf("the owner's log has no entry for this turn: %+v", blocks)
	}
	out := blockedTurnReport("I only asked for the release date", "Runbooks", blocks)
	if !strings.Contains(out, "I only asked for the release date") {
		t.Error("the reporter's own words were dropped")
	}
	// The rule and the warden's reason: owner-side facts, which is what they
	// have to judge.
	if !strings.Contains(out, "never mention project atlas") || !strings.Contains(out, "the draft named it") {
		t.Errorf("the owner cannot see what fired:\n%s", out)
	}
	if !strings.Contains(out, guardHookPreOutput) {
		t.Errorf("the hook is missing:\n%s", out)
	}
}

// Another session's blocks are not this report's.
func TestTheReportIsScopedToItsOwnTurn(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	a := sharedRunTurn(root, "alice", "bob", "a1")
	a.session = &ChatSession{ID: "s1"}
	a.recordGuardrailBlock("rule one", guardHookPreOutput, "first")
	b := sharedRunTurn(root, "alice", "bob", "a1")
	b.session = &ChatSession{ID: "s2"}
	b.recordGuardrailBlock("rule two", guardHookPreOutput, "second")

	got := blocksForTurn(UserDB(root, "alice"), "a1", "s2", 3)
	if len(got) != 1 || got[0].Rule != "rule two" {
		t.Fatalf("scoped to the wrong session: %+v", got)
	}
}

// A log that has rolled past the entry says so, rather than implying the rule
// is unknowable.
func TestAMissingEntrySaysSo(t *testing.T) {
	out := blockedTurnReport("this seems wrong", "Runbooks", nil)
	if !strings.Contains(out, "this seems wrong") {
		t.Error("the reporter's words were dropped")
	}
	if !strings.Contains(out, "block log") {
		t.Errorf("no pointer to where the owner can look:\n%s", out)
	}
}
