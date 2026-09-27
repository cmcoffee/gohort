package core

import (
	"context"
	"strings"
	"testing"
)

// A reply that opens mid-sentence carried on the user's message in their
// voice instead of answering it. It is struck, the model is asked again, and
// the answer that comes back is the reply.
func TestAReplyThatCarriesOnTheUsersMessageIsAskedAgain(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: ", its blocking longer output. just remove that property and re-publish it."},
		{Content: "Removed the limit and re-published the tool.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "remove the limit on the tool"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.struck) != 1 || !strings.HasPrefix(h.struck[0], "Retracted:") || !h.sawDiag("role-break-corrected") {
		t.Fatalf("the reply should be struck once with a reason, struck=%v diags=%v", h.struck, h.diags)
	}
	if stub.Calls() != 2 {
		t.Errorf("one correction, so two calls; got %d", stub.Calls())
	}
	if resp == nil || resp.Content != "Removed the limit and re-published the tool." {
		t.Errorf("the answer asked for is the reply, got %+v", resp)
	}
}

func TestOnlyAMidSentenceOpeningIsARoleBreak(t *testing.T) {
	for _, s := range []string{", and then", "  ; also"} {
		if !continuesUsersMessage(s) {
			t.Errorf("%q opens mid-sentence", s)
		}
	}
	for _, s := range []string{"yes.", "Done.", "ok, removed it", "", "...and that is it"} {
		if continuesUsersMessage(s) {
			t.Errorf("%q is an ordinary reply", s)
		}
	}
}
