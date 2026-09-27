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
	const open = "can you make sure that pipeline works correctly"
	for _, c := range []struct{ reply, asked string }{
		{", and then", "remove the limit on the tool"},
		{"  ; also", "anything"},
		{"for music generation and saves the result to the workspace?", open},
		{"and re-publish it", "remove the limit"},
	} {
		if !continuesUsersMessage(c.reply, c.asked) {
			t.Errorf("%q after %q carries on the message", c.reply, c.asked)
		}
	}
	for _, c := range []struct{ reply, asked string }{
		{"yes.", open},
		{"Done.", open},
		{"ok, removed it", open},
		{"For that, I ran the pipeline.", open},
		{"for sure, running it now", open + "?"},
		{"and it works", "does it work."},
		{"", open},
	} {
		if continuesUsersMessage(c.reply, c.asked) {
			t.Errorf("%q after %q is an ordinary reply", c.reply, c.asked)
		}
	}
}
