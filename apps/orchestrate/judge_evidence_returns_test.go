// Showing a judge what the tools returned is only half the fix. The excerpts
// are abridged — elided middles, oldest dropped — so a judge that reads a gap
// as an absence trades the old false convictions for a new kind. Both prompts
// have to say the returns ACQUIT and never convict.
package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestBothJudgePromptsSayTheReturnsOnlyAcquit(t *testing.T) {
	for _, p := range []struct{ name, prompt string }{
		{"turn judge", turnJudgeSysPrompt},
		{"grounding judge", groundingJudgeSysPrompt},
	} {
		if !strings.Contains(p.prompt, "RETURNED") {
			t.Errorf("%s: the prompt never mentions what the actions returned, so the new evidence goes unread", p.name)
		}
		if !strings.Contains(p.prompt, "ABRIDGED") {
			t.Errorf("%s: the prompt must say the excerpts are abridged, or a missing quote becomes a conviction", p.name)
		}
		if !strings.Contains(strings.ToLower(p.prompt), "not evidence") {
			t.Errorf("%s: the prompt must disarm the absence explicitly; naming the abridgement is not the same as forbidding the inference", p.name)
		}
	}
}

// The turn judge's evidence message is the thing asserted on without a model,
// so the returns block has to be visible there.
func TestTurnJudgeEvidenceCarriesTheReturns(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{
		Request:     "add those tools",
		Reply:       "Saved. Two names were rejected.",
		ToolCalls:   []string{"update_agent/save"},
		ToolOutputs: []string{"update_agent/save: AGENT_UPDATED ok. WARNING: dropped: recall, remember."},
	})
	if !strings.Contains(msg, "WHAT THOSE CALLS RETURNED") {
		t.Error("the evidence message does not render the returns block")
	}
	if !strings.Contains(msg, "dropped: recall, remember") {
		t.Error("the result text itself must reach the judge; a heading with no content is worse than nothing")
	}
	// Ordering matters: what ran and what it returned are one piece of
	// evidence, and a judge that meets the action list alone forms a verdict
	// before the returns arrive.
	if strings.Index(msg, "TOOL ACTIONS THE TURN RAN") > strings.Index(msg, "WHAT THOSE CALLS RETURNED") {
		t.Error("the returns must follow the action list, not precede it")
	}
}

// A turn that ran nothing renders no block at all. A heading over an empty list
// reads as a positive claim that the calls came back empty.
func TestNoReturnsBlockWhenNothingRan(t *testing.T) {
	msg := turnJudgeEvidenceMessage(TurnClaimEvidence{Request: "hi", Reply: "Hello."})
	if strings.Contains(msg, "WHAT THOSE CALLS RETURNED") {
		t.Error("no tools ran, so there is nothing to render")
	}
}
