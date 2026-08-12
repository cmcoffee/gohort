package orchestrate

// The judge is asked to distinguish "a duration the assistant made up" from one
// it was given — a distinction that is unanswerable from the reply alone, since
// the sentence reads identically either way. The evidence never mentioned that
// a number had been supplied, so the exception was dead text: the framework
// said "This usually takes about 13 seconds; say so if it is worth knowing",
// the model said so, and the machinery guard retracted the reply for saying it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestJudgeIsToldWhenTheWaitWasSupplied(t *testing.T) {
	ev := TurnClaimEvidence{
		Request:       "Can we edit this so the address is only on one pillar?",
		ToolCalls:     []string{"image"},
		Reply:         "That edit is running now, should be about 13 seconds, and I'll send it over when it lands.",
		Backgrounded:  true,
		GivenEstimate: "13 seconds",
	}
	msg := turnJudgeEvidenceMessage(ev)
	if !strings.Contains(msg, "13 seconds") {
		t.Fatal("the supplied wait never reaches the judge, so it cannot tell a given estimate from an invented one")
	}
	if !strings.Contains(msg, "NOT machinery") {
		t.Error("the evidence states the number but not that quoting it is allowed — the judge is left to guess")
	}

	// The system prompt's rule and the evidence have to line up, or the
	// exception is unreachable from either side.
	if !strings.Contains(turnJudgeSysPrompt, "made up rather than one it was given") {
		t.Error("the machinery rule no longer distinguishes an invented duration from a supplied one")
	}
}

func TestJudgeIsNotToldAboutAWaitThatWasNeverOffered(t *testing.T) {
	// No measured duration means detachedNotice tells the model to put NO time
	// on it. An estimate in the reply then really is invented, and must stay
	// flaggable — this is the case the machinery rule exists for.
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request: "make me a picture", ToolCalls: []string{"image"},
		Reply: "Should be about five minutes.", Backgrounded: true,
	})
	if strings.Contains(msg, "NOT machinery") {
		t.Error("the judge is excused an estimate the framework never gave — invented durations would stop being caught")
	}
	// And the backgrounded fact still lands, since the claim arm depends on it.
	if !strings.Contains(msg, "A BACKGROUND JOB WAS STARTED BY THIS TURN: yes") {
		t.Error("the backgrounded fact went missing from the evidence")
	}
}
