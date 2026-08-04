// The end-of-turn judge, and the filter that decides when it is worth paying
// for.
//
// The phrase-list guards must be precise, because they act directly: a false
// positive re-prompts an honest answer. The pre-filter can be sloppy, because
// all a false positive costs is one small model call that comes back KEPT.
// These tests pin that asymmetry — the filter is deliberately over-inclusive
// where the guards are careful, and evidence-shaped rather than wording-shaped
// so it does not simply re-select the turns the guards already catch.
package core

import "testing"

func TestTheJudgeLooksWhenActionsAndWordsCouldDisagree(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   TurnClaimEvidence
	}{
		{"said something, did nothing", TurnClaimEvidence{
			Reply: "On it — let me grab some reference photos and composite them in.",
		}},
		{"a bare answer with no tools is still looked at", TurnClaimEvidence{
			// The 22:41 turn: "Wiwee, try again" answered in 66 characters with
			// zero tool calls. No wording gate here on purpose — whatever it
			// said, nothing was tried, and only a reader can tell whether the
			// sentence admits that.
			Reply: "Yeah, that one's still not working out.",
		}},
		{"did something and it failed", TurnClaimEvidence{
			Reply: "Here's the picture you asked for.", ToolCalls: []string{"image"}, ToolErrors: 1,
		}},
		{"produced nothing and is delivering nothing", TurnClaimEvidence{
			Reply: "Here's you, wasting away in the garage like Craig ordered.",
			// The caption case. It slipped the noun rule entirely; the filter
			// never reads the words, only that an image tool ran and no file
			// is going out.
			ToolCalls: []string{"generate_image"}, Delivered: 0,
		}},
	} {
		if !turnClaimWorthJudging(c.ev) {
			t.Errorf("%s: must be judged", c.name)
		}
	}
}

func TestTheJudgeStaysOutOfTheWayOtherwise(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   TurnClaimEvidence
	}{
		{"nothing was said", TurnClaimEvidence{Reply: "   ", ToolCalls: nil}},
		{"a promise backed by a started job", TurnClaimEvidence{
			// The reply detachedNotice explicitly ASKS the model to write.
			// Judging it would convict the framework's own instruction.
			Reply: "I'll get that going and let you know when it's done.", Backgrounded: true,
		}},
		{"work ran, worked, and shipped", TurnClaimEvidence{
			Reply: "Here's the render.", ToolCalls: []string{"image"}, Delivered: 1,
		}},
		{"work ran and nothing about it produces files", TurnClaimEvidence{
			Reply: "Tokyo is 14°C and raining.", ToolCalls: []string{"web_search"},
		}},
	} {
		if turnClaimWorthJudging(c.ev) {
			t.Errorf("%s: must not cost a model call", c.name)
		}
	}
}

func TestAJudgeThatCannotAnswerConvictsNobody(t *testing.T) {
	ev := TurnClaimEvidence{Reply: "On it — let me grab those."}

	// No hook at all is the shape every host had before this existed.
	if _, convicted := judgeTurnClaim(AgentLoopConfig{}, ev); convicted {
		t.Error("no judge must convict nobody")
	}
	// ok=false is "I could not answer", NOT "kept" and NOT "unkept". A judge
	// that errored has cleared nothing and accused nobody.
	cfg := AgentLoopConfig{TurnClaimJudge: func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: true, Claim: "x"}, false
	}}
	if _, convicted := judgeTurnClaim(cfg, ev); convicted {
		t.Error("a judge that could not answer must not convict")
	}
	// An acquittal is an acquittal.
	cfg.TurnClaimJudge = func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: false}, true
	}
	if _, convicted := judgeTurnClaim(cfg, ev); convicted {
		t.Error("KEPT must not convict")
	}
	// And a real conviction carries its quote through.
	cfg.TurnClaimJudge = func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return TurnClaimVerdict{Unkept: true, Claim: "On it", Why: "no tool ran"}, true
	}
	v, convicted := judgeTurnClaim(cfg, ev)
	if !convicted || v.Claim != "On it" || v.Why != "no tool ran" {
		t.Errorf("a conviction must carry its quote and reason, got %+v convicted=%v", v, convicted)
	}
}

func TestTheJudgeIsNeverConsultedWhenTheFilterSaysNo(t *testing.T) {
	// The filter is what keeps this from being a tax on every reply.
	called := false
	cfg := AgentLoopConfig{TurnClaimJudge: func(TurnClaimEvidence) (TurnClaimVerdict, bool) {
		called = true
		return TurnClaimVerdict{Unkept: true, Claim: "x"}, true
	}}
	judgeTurnClaim(cfg, TurnClaimEvidence{Reply: "Here's the render.", ToolCalls: []string{"image"}, Delivered: 1})
	if called {
		t.Error("a turn that delivered what it made must not cost a model call")
	}
}

func TestMissingEvidenceReadsTheSuspiciousWay(t *testing.T) {
	// A host that wires no DeliveredCount reports zero deliveries, which makes
	// a delivery claim MORE suspect rather than less. Absent evidence must
	// never quietly acquit.
	if n := (AgentLoopConfig{}).deliveredCount(); n != 0 {
		t.Errorf("absent delivery count = %d, want 0", n)
	}
	if (AgentLoopConfig{}).backgrounded() {
		t.Error("absent background signal must not excuse a promise")
	}
}
