// The pre-filter decides how often a grounding judge costs a model call, and
// the scope decides what it may convict. Both are cheap to get wrong in the
// expensive direction: a filter that always fires judges every turn, and a
// scope that admits anything teaches the model to hedge.
package core

import "testing"

func groundingEv(reply string, unchecked ...string) TurnGroundingEvidence {
	return TurnGroundingEvidence{Reply: reply, Unchecked: unchecked}
}

// Nothing marked means nothing to judge — the common case, and it must not
// reach a model.
func TestNoUncheckedNotesNeverJudges(t *testing.T) {
	if turnGroundingWorthJudging(groundingEv("the server runs 22.04")) {
		t.Error("with no unchecked notes there is nothing in scope")
	}
	if turnGroundingWorthJudging(TurnGroundingEvidence{Unchecked: []string{"the server runs 22.04"}}) {
		t.Error("an empty reply has nothing to assert")
	}
}

// A reply that shares no distinctive word with any marked note is not
// discussing them.
func TestUnrelatedReplyIsNotJudged(t *testing.T) {
	ev := groundingEv("Sure — I've drafted the email and it's ready to send.",
		"the staging server runs Ubuntu 22.04")
	if turnGroundingWorthJudging(ev) {
		t.Errorf("unrelated reply should not be judged: %q", ev.Reply)
	}
}

func TestReplyTouchingAMarkedNoteIsJudged(t *testing.T) {
	ev := groundingEv("The staging box is on Ubuntu, so that package will work.",
		"the staging server runs Ubuntu 22.04")
	if !turnGroundingWorthJudging(ev) {
		t.Error("a reply using a distinctive word from a marked note should be judged")
	}
}

// Matching on common words is the same as having no filter.
func TestCommonWordsDoNotTriggerJudging(t *testing.T) {
	ev := groundingEv("There are other users, and these should still work.",
		"the user's cluster has three nodes")
	if turnGroundingWorthJudging(ev) {
		t.Errorf("stop words must not make every reply worth judging: %q", ev.Reply)
	}
}

// A conviction the correction cannot use is worse than none: it would tell the
// model its reply says "".
func TestConvictionWithoutAQuoteIsNoOpinion(t *testing.T) {
	cfg := AgentLoopConfig{
		UncheckedClaims: []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: func(TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			return TurnGroundingVerdict{Asserted: true, Claim: "  "}, true
		},
	}
	if _, ok := judgeTurnGrounding(cfg, groundingEv("The staging server runs Ubuntu.",
		"the staging server runs Ubuntu 22.04")); ok {
		t.Error("an unquotable conviction must not reach the correction")
	}
}

// A judge that could not answer has not cleared anything, and must not be read
// as a conviction either.
func TestJudgeFailureIsNoOpinion(t *testing.T) {
	cfg := AgentLoopConfig{
		TurnGroundingJudge: func(TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			return TurnGroundingVerdict{Asserted: true, Claim: "x"}, false
		},
	}
	if _, ok := judgeTurnGrounding(cfg, groundingEv("The staging server runs Ubuntu.",
		"the staging server runs Ubuntu 22.04")); ok {
		t.Error("ok=false is no opinion, not a conviction")
	}
}

// No judge configured is every host that has not opted in.
func TestNilJudgeIsInert(t *testing.T) {
	if _, ok := judgeTurnGrounding(AgentLoopConfig{}, groundingEv("The staging server runs Ubuntu.",
		"the staging server runs Ubuntu 22.04")); ok {
		t.Error("a nil judge must convict nothing")
	}
}

func TestCleanVerdictPasses(t *testing.T) {
	cfg := AgentLoopConfig{
		TurnGroundingJudge: func(TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			return TurnGroundingVerdict{}, true
		},
	}
	if _, ok := judgeTurnGrounding(cfg, groundingEv("You mentioned the staging server runs Ubuntu 22.04.",
		"the staging server runs Ubuntu 22.04")); ok {
		t.Error("an attributed repetition is exactly what the rule asks for")
	}
}

// Scope comes from the same predicate the marker uses, so the judge cannot
// drift from what the model was actually shown.
func TestUncheckedNotesMatchWhatWasMarked(t *testing.T) {
	facts := []MemoryFact{
		{Note: "the cluster has three nodes", MemoryProvenance: MemoryProvenance{Source: MemSourceObserved, Domain: ClaimWorld}},
		{Note: "prefers snake_case", MemoryProvenance: MemoryProvenance{Source: MemSourceUserStated, Domain: ClaimSelf}},
		{Note: "release notes list v2.1", MemoryProvenance: MemoryProvenance{Source: MemSourceRetrieved, Domain: ClaimWorld}},
	}
	got := UncheckedFactNotes(facts)
	if len(got) != 1 || got[0] != "the cluster has three nodes" {
		t.Errorf("scope should be exactly the marked notes, got %v", got)
	}
}
