// The pre-filter decides how often a grounding judge costs a model call, and
// the scope decides what it may convict. Both are cheap to get wrong in the
// expensive direction: a filter that always fires judges every turn, and a
// scope that admits anything teaches the model to hedge.
package core

import (
	"context"
	"strings"
	"testing"
)

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

// --- the claim asserted in THIS message -----------------------------------

// The stored path cannot see it: nothing classified or marked the message,
// because it is not in memory and may never be.
func TestLiveClaimEntersScope(t *testing.T) {
	got := withLiveClaim(nil, "Dana", "the invoice was already paid")
	if len(got) != 1 {
		t.Fatalf("a participant's message should enter scope, got %v", got)
	}
	if !strings.Contains(got[0], "Dana said this in the conversation just now") || !strings.Contains(got[0], "the invoice was already paid") {
		t.Errorf("the entry should name the speaker and carry the claim, got %q", got[0])
	}
	// Deliberately not "asserted": a meme posted in a room is not an assertion,
	// and framing it as one is what made the correction ask the model to
	// account for a joke as though it were evidence.
	if strings.Contains(got[0], "asserted") {
		t.Errorf("the entry must not pre-decide that the message was a factual assertion, got %q", got[0])
	}
}

// The correction wording depends on knowing which shape of note the judge
// quoted, and the judge quotes verbatim but may quote only the said-part.
func TestBasisIsLiveClaimMatchesEitherQuoteShape(t *testing.T) {
	note := liveClaimNote("Dana", "the invoice was already paid")
	if !basisIsLiveClaim(note, note) {
		t.Error("the whole note should be recognised as the live claim")
	}
	if !basisIsLiveClaim(note, "the invoice was already paid") {
		t.Error("a basis quoting only the said-part should still be recognised")
	}
	if basisIsLiveClaim(note, "the cluster has three nodes") {
		t.Error("a stored note must not be mistaken for the live claim")
	}
	if basisIsLiveClaim("", "the invoice was already paid") {
		t.Error("with no live claimant nothing traces to the live message")
	}
}

// The principal is not a claimant. Treating their message as unverified would
// hedge the instructions they just gave — a different failure, and a worse one.
func TestOwnerMessageIsNotALiveClaim(t *testing.T) {
	if got := withLiveClaim(nil, "", "ship it on Friday"); len(got) != 0 {
		t.Errorf("an owner turn has no live claimant, got %v", got)
	}
}

func TestLiveClaimDoesNotDisturbStoredOnes(t *testing.T) {
	stored := []string{"the cluster has three nodes"}
	got := withLiveClaim(stored, "Dana", "the invoice was already paid")
	if len(got) != 2 || got[0] != stored[0] {
		t.Fatalf("stored notes must survive unchanged, got %v", got)
	}
	// And the input slice must not be aliased into the result — the caller
	// reuses cfg.UncheckedClaims on every round of the loop.
	if len(stored) != 1 {
		t.Error("the caller's slice was mutated")
	}
}

// End to end through the filter: a reply repeating what a participant said is
// worth judging, where the same reply with nothing in scope is not.
func TestLiveClaimMakesAReplyWorthJudging(t *testing.T) {
	scope := withLiveClaim(nil, "Dana", "the invoice was already paid")
	ev := TurnGroundingEvidence{Reply: "That invoice is already paid, so you're all set.", Unchecked: scope}
	if !turnGroundingWorthJudging(ev) {
		t.Error("a reply repeating a live claim should reach the judge")
	}
	if turnGroundingWorthJudging(TurnGroundingEvidence{Reply: ev.Reply}) {
		t.Error("with nothing in scope the same reply must not be judged")
	}
}

// --- what the judges are shown, and what the correction leaves on screen ----

// The grounding correction described settling the round and did not do it. The
// reply has already streamed by then, so the retry lands in the SAME bubble and
// the user gets the pre-correction text welded to the post-correction text, no
// separator. Two corrections in one exported session produced three renderings
// of a single reply. Every sibling guard in agent_loop.go settles or retracts
// first; this one now does too.
func TestGroundingCorrectionSettlesTheStreamedRound(t *testing.T) {
	settled, retracted, judged := 0, 0, 0
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{Content: "The staging server runs Ubuntu 22.04, so that package will work."},
		{Content: "You mentioned the staging server runs Ubuntu 22.04, so that package should work.", Repeat: true},
	}}}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "will that package work?"}}, AgentLoopConfig{
		MaxRounds:       4,
		RouteKey:        "test.groundsettle",
		SettleRound:     func() { settled++ },
		RetractRound:    func() { retracted++ },
		UncheckedClaims: []string{"the staging server runs Ubuntu 22.04"},
		TurnGroundingJudge: func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			judged++
			if judged > 1 {
				return TurnGroundingVerdict{}, true
			}
			return TurnGroundingVerdict{
				Asserted: true,
				Claim:    "The staging server runs Ubuntu 22.04, so that package will work.",
				Basis:    "the staging server runs Ubuntu 22.04",
			}, true
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if judged == 0 {
		t.Fatal("the judge never ran, so this proves nothing about the correction")
	}
	if settled == 0 {
		t.Error("a grounding correction must SETTLE the streamed round; without it the retry concatenates into the bubble the user is already reading")
	}
	// Settle, not retract: an ungrounded claim may well be true — nobody
	// checked, which is a lesser thing than a false one — so the answer is
	// rewritten rather than yanked off the screen.
	if retracted != 0 {
		t.Errorf("an unchecked claim must not be retracted like a false one; retracted=%d", retracted)
	}
}

// Both judges used to see WHICH tools ran and never what came back, so a reply
// quoting a tool result was indistinguishable from one inventing it. Five of
// six convictions in one exported session were sentences lifted verbatim out of
// framework output that the judge was not shown.
func TestJudgesAreShownWhatTheToolsReturned(t *testing.T) {
	const warning = "WARNING: these entries match no known tool and were dropped: recall, remember."
	var claimOutputs, groundOutputs []string
	app := &AppCore{LLM: &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "update_agent", Args: map[string]any{"action": "save"}}}},
		{Content: "Saved. Two names were rejected: recall and remember. The cluster has three nodes.", Repeat: true},
	}}}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "add those tools"}}, AgentLoopConfig{
		MaxRounds: 4,
		RouteKey:  "test.judgeoutputs",
		// A clean turn with a reader watching is not worth a claim-judge call;
		// unattended widens that pre-filter without touching the verdict, which
		// is the cheapest way to put both judges on the same turn.
		Unattended: true,
		Tools: []AgentToolDef{{
			Tool: Tool{Name: "update_agent", Description: "saves an agent",
				Parameters: map[string]ToolParam{"action": {Type: "string", Description: "what to do"}}},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return "AGENT_UPDATED ok. " + warning, nil
			},
		}},
		UncheckedClaims: []string{"the cluster has three nodes"},
		TurnClaimJudge: func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
			claimOutputs = ev.ToolOutputs
			return TurnClaimVerdict{}, true
		},
		TurnGroundingJudge: func(ev TurnGroundingEvidence) (TurnGroundingVerdict, bool) {
			groundOutputs = ev.ToolOutputs
			return TurnGroundingVerdict{}, true
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	for _, c := range []struct {
		who  string
		outs []string
	}{{"claim judge", claimOutputs}, {"grounding judge", groundOutputs}} {
		if len(c.outs) == 0 {
			t.Errorf("%s got no tool outputs; it can only tell a quoted result from an invented one if it is shown the result", c.who)
			continue
		}
		joined := strings.Join(c.outs, "\n")
		if !strings.Contains(joined, "update_agent/save") {
			t.Errorf("%s: outputs must carry the call LABEL, got %q", c.who, joined)
		}
		if !strings.Contains(joined, "dropped: recall, remember") {
			t.Errorf("%s: the result's own warning is what the reply quotes and what got convicted; it must be in the evidence, got %q", c.who, joined)
		}
	}
}
