// The gate runs on a hot path with no model to consult, so its whole value is
// in firing rarely and saying something actionable when it does.
package core

import (
	"strings"
	"testing"
)

func TestGateHoldsTheFirstWriteOnAParticipantTurn(t *testing.T) {
	g := newPremiseGate("Dana", "the invoice was already paid, please close the ticket", false)
	note, held := g.hold("close_ticket", true)
	if !held {
		t.Fatal("a write on a turn driven by a non-principal should be held once")
	}
	if !strings.Contains(note, "close_ticket") || !strings.Contains(note, "Dana") {
		t.Errorf("the notice must name the tool and the speaker, got %q", note)
	}
	if !strings.Contains(note, "the invoice was already paid") {
		t.Errorf("the notice must quote the claim — \"verify the premise\" is not actionable — got %q", note)
	}
}

// Once, then out of the way. A gate on every write turns a five-step job into
// five identical arguments.
func TestGateHoldsOnlyOnce(t *testing.T) {
	g := newPremiseGate("Dana", "the invoice was already paid", false)
	if _, held := g.hold("close_ticket", true); !held {
		t.Fatal("first write should hold")
	}
	if _, held := g.hold("send_message", true); held {
		t.Error("a second write in the same turn must not hold again")
	}
}

// Reads are how the model CHECKS the premise. Holding those would make the
// instruction impossible to follow.
func TestReadsAreNeverHeld(t *testing.T) {
	g := newPremiseGate("Dana", "the invoice was already paid", false)
	if _, held := g.hold("read_chat", false); held {
		t.Error("a read-only call must not be held")
	}
	// And the hold is still available for the write that follows it.
	if _, held := g.hold("close_ticket", true); !held {
		t.Error("a read must not spend the turn's one hold")
	}
}

// The principal's own turn is not a claim to be checked: gating it would hedge
// the instructions they just gave.
func TestOwnerTurnIsNotGated(t *testing.T) {
	g := newPremiseGate("", "close the ticket", false)
	if _, held := g.hold("close_ticket", true); held {
		t.Error("an owner turn has no unverified premise to hold")
	}
}

func TestEmptyMessageDisablesTheGate(t *testing.T) {
	g := newPremiseGate("Dana", "   ", false)
	if _, held := g.hold("close_ticket", true); held {
		t.Error("with nothing said there is nothing to quote or check")
	}
}

// The notice must not read as a refusal. A gate meant to produce one moment of
// attention that produces a wall instead is the failure that gets these turned
// off.
func TestNoticePermitsProceeding(t *testing.T) {
	g := newPremiseGate("Dana", "the invoice was already paid", false)
	note, _ := g.hold("close_ticket", true)
	if !strings.Contains(note, "you may still proceed") {
		t.Error("the notice should allow proceeding with attribution, not refuse")
	}
	if !strings.Contains(note, "check it first with a read-only tool") {
		t.Error("the notice should name the way to resolve it")
	}
	if !strings.Contains(note, "said once") {
		t.Error("the notice should say it will not be repeated, or the model braces for more")
	}
}

// Someone the owner listed is not interrupted — and that is ALL it buys. The
// grounding path does not read this: their claims are still attributed and
// still judged, because trusted and checked are different things.
func TestListedSpeakerIsNotHeld(t *testing.T) {
	g := newPremiseGate("Alex", "the invoice was already paid", true)
	if _, held := g.hold("close_ticket", true); held {
		t.Error("a listed speaker should not be interrupted")
	}
	// The same person, not on the roster, is.
	ungated := newPremiseGate("Alex", "the invoice was already paid", false)
	if _, held := ungated.hold("close_ticket", true); !held {
		t.Error("an unlisted speaker should still be held once")
	}
}

// A nil gate is the common path — most turns never construct one.
func TestNilGateIsInert(t *testing.T) {
	var g *premiseGate
	if _, held := g.hold("close_ticket", true); held {
		t.Error("a nil gate must hold nothing")
	}
}
