package core

// The loop's half of the phantom-delivery fix: a reply promising a file that
// does not exist gets its claim removed and one correction round, the same
// remedy a fake tool call gets. Both are an action claimed but never taken.

import (
	"context"
	"strings"
	"testing"
)

func TestDeliveryMarkersAreStrippedFromAClaim(t *testing.T) {
	got := StripDeliveryMarkers("Here you go! [ATTACH: find-dkfindcraig.jpg]")
	if strings.Contains(got, "ATTACH") {
		t.Errorf("the claim must not survive: %q", got)
	}
	if !strings.Contains(got, "Here you go!") {
		t.Errorf("the sentence around it should remain: %q", got)
	}
	// A reply that was ONLY the marker strips to nothing, which is the state the
	// correction round exists to replace.
	if got := StripDeliveryMarkers("[ATTACH: nope.png]"); got != "" {
		t.Errorf("a bare marker should strip to empty, got %q", got)
	}
	// Several markers, all removed.
	if got := StripDeliveryMarkers("[ATTACH: a.png] and [ATTACH: b.png]"); strings.Contains(got, "ATTACH") {
		t.Errorf("every marker must go: %q", got)
	}
}

func TestTheLoopAsksTheHostAndDefaultsToSilence(t *testing.T) {
	// No hook = no check, which is exactly how every host behaved before this
	// existed — a nil hook must never be a panic or a false positive.
	if refs := phantomDeliveryRefs(AgentLoopConfig{}, "[ATTACH: whatever.png]"); refs != nil {
		t.Errorf("a host with no hook reports nothing, got %v", refs)
	}
	cfg := AgentLoopConfig{PhantomDeliveryRefs: func(string) []string { return []string{"ghost.png"} }}
	if refs := phantomDeliveryRefs(cfg, "[ATTACH: ghost.png]"); len(refs) != 1 {
		t.Errorf("the host's answer should come back, got %v", refs)
	}
	// Empty content is never a claim.
	if refs := phantomDeliveryRefs(cfg, "   "); refs != nil {
		t.Errorf("empty content asks nothing, got %v", refs)
	}
}

// The host's finish check holds a reply back, strikes it with the host's
// reason, hands the model the notice, and accepts the next reply once the host
// is satisfied. Observed: an authoring agent answered "has been fixed" over a
// tool it had edited and never run.
func TestAFinishCheckHoldsTheReplyUntilTheHostIsSatisfied(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "Fixed it, the tool works now."},
		{Content: "I changed the tool but have not run it yet.", Repeat: true},
	}}
	checks, struck := 0, ""
	app := &AppCore{LLM: stub, LeadLLM: stub}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "fix the tool"}}, AgentLoopConfig{
		MaxRounds:   6,
		StrikeRound: func(reason string) { struck = reason },
		FinishCheck: func(reply string) (string, string) {
			checks++
			if checks == 1 {
				return "tool x is NOT verified: edited since it last passed", "Held back: x not verified."
			}
			return "", ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !strings.Contains(resp.Content, "have not run it") {
		t.Fatalf("the reply after the check should be the one that stands, got %+v", resp)
	}
	if struck != "Held back: x not verified." {
		t.Errorf("the held-back reply should be struck with the host's reason, got %q", struck)
	}
	if n := stub.Calls(); n != 2 {
		t.Fatalf("one correction round expected, the model was called %d times", n)
	}
	last := stub.Sent(1)
	if got := last[len(last)-1].Content; !strings.Contains(got, "NOT verified") {
		t.Errorf("the model should be handed the host's notice, got %q", got)
	}
}

// A check that keeps objecting still lets the turn end: it is budgeted like
// every other correction.
func TestAFinishCheckThatNeverClearsStillEndsTheTurn(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{{Content: "Done.", Repeat: true}}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		MaxRounds:   10,
		FinishCheck: func(string) (string, string) { return "still not verified", "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := stub.Calls(); n != 1+maxCorrectionsPerKind {
		t.Errorf("the check should re-prompt %d times and then let the reply stand, model called %d times", maxCorrectionsPerKind, n)
	}
}
